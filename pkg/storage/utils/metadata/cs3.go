// Copyright 2018-2022 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"

	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocdav/net"
	"github.com/owncloud/reva/v2/pkg/appctx"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	rtrace "github.com/owncloud/reva/v2/pkg/trace"
	"github.com/owncloud/reva/v2/pkg/utils"
)

const tracerName = "github.com/owncloud/reva/pkg/storage/utils/metadata"

// startSpan starts a span with the tracer provider of the context. The gRPC
// and HTTP servers of reva put the provider of their service into the
// context. Prefer it to the global provider: in the single binary, each
// service sets the global provider at its start, so it belongs to the service
// that started last. A context with no provider, for example at a start,
// uses the global provider.
func startSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return rtrace.ContextGetTracerProviderOrDefault(ctx).Tracer(tracerName).Start(ctx, name)
}

// defaultAuthRPCTimeout bounds the machine auth Authenticate call made for
// every metadata operation. It only has to cover a token mint, so it is
// generous - its purpose is to turn a stalled gateway into an error instead of
// a goroutine that waits for the lifetime of the process.
const defaultAuthRPCTimeout = 10 * time.Second

// dataGatewayMaxIdleConns is the number of idle connections that the data
// gateway client keeps for each host. The default of Go is 2. The metadata
// storage sends many parallel requests to one host. With the default, the
// transport closes the extra connections after each request and opens new
// ones for the next requests. This wastes ports.
const dataGatewayMaxIdleConns = 100

// dataGatewayIdleConnTimeout is the time after which the data gateway client
// closes an idle connection. Servers and load balancers on the way often close
// idle connections after 60 seconds. The client must close them first.
// Otherwise it sends a request on a closed connection, and the transport does
// not repeat a PUT or POST request.
const dataGatewayIdleConnTimeout = 30 * time.Second

// dataGatewayTransport is the transport of all data gateway clients. All CS3
// instances share it, so they share one pool of idle connections.
var dataGatewayTransport = newDataGatewayTransport()

// newDataGatewayTransport returns a copy of the default transport of Go with a
// larger pool of idle connections for each host.
func newDataGatewayTransport() *http.Transport {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr.MaxIdleConnsPerHost = dataGatewayMaxIdleConns
	tr.IdleConnTimeout = dataGatewayIdleConnTimeout
	if tr.MaxIdleConns != 0 && tr.MaxIdleConns < dataGatewayMaxIdleConns {
		tr.MaxIdleConns = dataGatewayMaxIdleConns
	}
	return tr
}

// CS3 represents a metadata storage with a cs3 storage backend
type CS3 struct {
	SpaceRoot *provider.ResourceId

	providerAddr      string
	gatewayAddr       string
	useSystemUser     bool
	serviceUser       *user.User
	machineAuthAPIKey string
	authRPCTimeout    time.Duration

	dataGatewayClient *http.Client
}

// NewCS3 returns a new CS3 instance. Use an authenticated context and be sure to define SpaceRoot manually.
func NewCS3(gwAddr, providerAddr string) (s *CS3) {
	return &CS3{
		providerAddr:      providerAddr,
		gatewayAddr:       gwAddr,
		authRPCTimeout:    defaultAuthRPCTimeout,
		dataGatewayClient: &http.Client{Transport: dataGatewayTransport},
	}
}

// NewCS3Storage returns a new cs3 storage instance. Context passed to methods is irrelevant as the service user will be used.
// Be sure to call Init before using the storage.
func NewCS3Storage(gwAddr, providerAddr, serviceUserID, serviceUserIDP, machineAuthAPIKey string) (s Storage, err error) {
	cs3 := NewCS3(gwAddr, providerAddr)

	cs3.useSystemUser = true
	cs3.machineAuthAPIKey = machineAuthAPIKey
	cs3.serviceUser = &user.User{
		Id: &user.UserId{
			OpaqueId: serviceUserID,
			Idp:      serviceUserIDP,
		},
	}

	return cs3, nil
}

// Backend returns the backend name of the storage
func (cs3 *CS3) Backend() string {
	return "cs3"
}

// Init creates the metadata space
func (cs3 *CS3) Init(ctx context.Context, spaceid string) (err error) {
	ctx, span := startSpan(ctx, "Init")
	defer span.End()

	client, err := cs3.spacesClient()
	if err != nil {
		return err
	}

	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return err
	}

	lsRes, err := client.ListStorageSpaces(ctx, &provider.ListStorageSpacesRequest{
		Filters: []*provider.ListStorageSpacesRequest_Filter{
			{
				Type: provider.ListStorageSpacesRequest_Filter_TYPE_ID,
				Term: &provider.ListStorageSpacesRequest_Filter_Id{
					Id: &provider.StorageSpaceId{OpaqueId: spaceid + "!" + spaceid},
				},
			},
		},
	})
	switch {
	case err != nil:
		return err
	case lsRes.Status.Code == rpc.Code_CODE_OK && len(lsRes.StorageSpaces) > 0:
		if len(lsRes.StorageSpaces) > 0 {
			cs3.SpaceRoot = lsRes.StorageSpaces[0].Root
			return nil
		}
	}

	// FIXME change CS3 api to allow sending a space id
	cssr, err := client.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{
		Opaque: &types.Opaque{
			Map: map[string]*types.OpaqueEntry{
				"spaceid": {
					Decoder: "plain",
					Value:   []byte(spaceid),
				},
			},
		},
		Owner: cs3.serviceUser,
		Name:  "Metadata",
		Type:  "metadata",
	})
	switch {
	case err != nil:
		return err
	case cssr.Status.Code == rpc.Code_CODE_OK:
		cs3.SpaceRoot = cssr.StorageSpace.Root
	case cssr.Status.Code == rpc.Code_CODE_ALREADY_EXISTS:
		return errtypes.AlreadyExists(fmt.Sprintf("user %s does not have access to metadata space %s, but it exists", cs3.serviceUser.Id.OpaqueId, spaceid))
	default:
		return errtypes.NewErrtypeFromStatus(cssr.Status)
	}
	return nil
}

// SimpleUpload uploads a file to the metadata storage
func (cs3 *CS3) SimpleUpload(ctx context.Context, uploadpath string, content []byte) error {
	ctx, span := startSpan(ctx, "SimpleUpload")
	defer span.End()

	log := appctx.GetLogger(ctx)
	log.Debug().Str("path", uploadpath).Msg("cs3.SimpleUpload.start")
	_, err := cs3.Upload(ctx, UploadRequest{
		Path:    uploadpath,
		Content: content,
	})
	return err
}

// Upload uploads a file to the metadata storage
func (cs3 *CS3) Upload(ctx context.Context, req UploadRequest) (_ *UploadResponse, err error) {
	ctx, span := startSpan(ctx, "Upload")
	defer span.End()
	httpStatus := 0
	defer func() {
		endMetadataSpan(span, req.Path, httpStatus, len(req.Content), err,
			attribute.String("metadata.condition", uploadCondition(req)),
			attribute.Bool("metadata.conflict", metadataResult(err) == resultConflict))
	}()

	log := appctx.GetLogger(ctx)
	log.Debug().Str("path", req.Path).Msg("cs3.Upload.start")

	client, err := cs3.providerClient()
	if err != nil {
		return nil, err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return nil, err
	}

	ifuReq := &provider.InitiateFileUploadRequest{
		Opaque: &types.Opaque{},
		Ref: &provider.Reference{
			ResourceId: cs3.SpaceRoot,
			Path:       utils.MakeRelativePath(req.Path),
		},
	}

	if req.IfMatchEtag != "" {
		ifuReq.Options = &provider.InitiateFileUploadRequest_IfMatch{
			IfMatch: req.IfMatchEtag,
		}
	}
	if len(req.IfNoneMatch) > 0 {
		if req.IfNoneMatch[0] == "*" {
			ifuReq.Options = &provider.InitiateFileUploadRequest_IfNotExist{
				IfNotExist: true,
			}
		}
		// else {
		//   the http upload will carry all if-not-match etags
		// }
	}
	if req.IfUnmodifiedSince != (time.Time{}) {
		ifuReq.Options = &provider.InitiateFileUploadRequest_IfUnmodifiedSince{
			IfUnmodifiedSince: utils.TimeToTS(req.IfUnmodifiedSince),
		}
	}
	if req.MTime != (time.Time{}) {
		// The format of the X-OC-Mtime header is <epoch>.<nanoseconds>, e.g. '1691053416.934129485'
		ifuReq.Opaque = utils.AppendPlainToOpaque(ifuReq.Opaque, "X-OC-Mtime", utils.TimeToOCMtime(req.MTime))
	}

	ifuReq.Opaque = utils.AppendPlainToOpaque(ifuReq.Opaque, net.HeaderUploadLength, strconv.FormatInt(int64(len(req.Content)), 10))

	res, err := client.InitiateFileUpload(ctx, ifuReq)
	if err != nil {
		return nil, err
	}
	if res.Status.Code != rpc.Code_CODE_OK {
		return nil, errtypes.NewErrtypeFromStatus(res.Status)
	}

	var endpoint string

	for _, proto := range res.GetProtocols() {
		if proto.Protocol == "simple" {
			endpoint = proto.GetUploadEndpoint()
			break
		}
	}
	if endpoint == "" {
		return nil, errors.New("metadata storage doesn't support the simple upload protocol")
	}

	log.Debug().Str("path", req.Path).Str("endpoint", endpoint).Msg("cs3.Upload.initiate_done")

	httpReq, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(req.Content))
	if err != nil {
		return nil, err
	}
	for _, etag := range req.IfNoneMatch {
		httpReq.Header.Add(net.HeaderIfNoneMatch, etag)
	}

	md, _ := metadata.FromOutgoingContext(ctx)
	httpReq.Header.Add(ctxpkg.TokenHeader, md.Get(ctxpkg.TokenHeader)[0])
	// Send the trace context. Then the work of the data server is in the trace of the request.
	rtrace.Propagator.Inject(ctx, propagation.HeaderCarrier(httpReq.Header))
	resp, err := cs3.dataGatewayClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	httpStatus = resp.StatusCode
	log.Debug().Str("path", req.Path).Int("status", resp.StatusCode).Msg("cs3.Upload.put_done")
	if err := errtypes.NewErrtypeFromHTTPStatusCode(resp.StatusCode, httpReq.URL.Path); err != nil {
		return nil, err
	}
	etag := resp.Header.Get("Etag")
	if ocEtag := resp.Header.Get("OC-ETag"); ocEtag != "" {
		etag = ocEtag
	}
	log.Debug().Str("path", req.Path).Str("etag", etag).Msg("cs3.Upload.complete")
	return &UploadResponse{
		Etag:   etag,
		FileID: resp.Header.Get("OC-Fileid"),
	}, nil
}

// Values of the span attribute metadata.result.
const (
	resultOK          = "200"
	resultNotModified = "304"
	resultNotFound    = "404"
	resultConflict    = "conflict"
	resultError       = "error"
)

// metadataResult classifies the result of a download or an upload. A 304, a
// 404 and a CAS conflict are expected results, not errors.
func metadataResult(err error) string {
	switch err.(type) {
	case nil:
		return resultOK
	case errtypes.NotModified:
		return resultNotModified
	case errtypes.NotFound:
		return resultNotFound
	case errtypes.Aborted, errtypes.PreconditionFailed, errtypes.AlreadyExists, errtypes.TooEarly:
		return resultConflict
	default:
		return resultError
	}
}

// uploadCondition returns the CAS condition of an upload.
func uploadCondition(req UploadRequest) string {
	switch {
	case req.IfMatchEtag != "":
		return "if-match"
	case len(req.IfNoneMatch) > 0:
		return "if-none-match"
	case !req.IfUnmodifiedSince.IsZero():
		return "if-unmodified-since"
	default:
		return "none"
	}
}

// endMetadataSpan adds the result of a metadata call to the span. Then a
// trace shows a 304, a 404 or a CAS conflict. httpStatus is 0 when the call
// did not get an HTTP answer. size is -1 when the call transfers no content.
func endMetadataSpan(span trace.Span, path string, httpStatus, size int, err error, attrs ...attribute.KeyValue) {
	result := metadataResult(err)
	span.SetAttributes(
		attribute.String("metadata.path", path),
		attribute.String("metadata.result", result),
	)
	if size >= 0 {
		span.SetAttributes(attribute.Int("metadata.bytes", size))
	}
	if httpStatus != 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", httpStatus))
	}
	span.SetAttributes(attrs...)
	if result == resultError {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// Stat returns the metadata for the given path
func (cs3 *CS3) Stat(ctx context.Context, path string) (_ *provider.ResourceInfo, err error) {
	ctx, span := startSpan(ctx, "Stat")
	defer span.End()
	defer func() { endMetadataSpan(span, path, 0, -1, err) }()

	client, err := cs3.providerClient()
	if err != nil {
		return nil, err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return nil, err
	}

	req := provider.StatRequest{
		Ref: &provider.Reference{
			ResourceId: cs3.SpaceRoot,
			Path:       utils.MakeRelativePath(path),
		},
	}

	res, err := client.Stat(ctx, &req)
	if err != nil {
		return nil, err
	}
	if res.Status.Code != rpc.Code_CODE_OK {
		return nil, errtypes.NewErrtypeFromStatus(res.Status)
	}

	return res.Info, nil
}

// SimpleDownload reads a file from the metadata storage
func (cs3 *CS3) SimpleDownload(ctx context.Context, downloadpath string) (content []byte, err error) {
	ctx, span := startSpan(ctx, "SimpleDownload")
	defer span.End()
	dres, err := cs3.Download(ctx, DownloadRequest{Path: downloadpath})
	if err != nil {
		return nil, err
	}
	return dres.Content, nil
}

// Download reads a file from the metadata storage
func (cs3 *CS3) Download(ctx context.Context, req DownloadRequest) (out *DownloadResponse, err error) {
	ctx, span := startSpan(ctx, "Download")
	defer span.End()
	httpStatus := 0
	defer func() {
		size := 0
		if out != nil {
			size = len(out.Content)
		}
		endMetadataSpan(span, req.Path, httpStatus, size, err)
	}()

	client, err := cs3.providerClient()
	if err != nil {
		return nil, err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return nil, err
	}

	dreq := provider.InitiateFileDownloadRequest{
		Ref: &provider.Reference{
			ResourceId: cs3.SpaceRoot,
			Path:       utils.MakeRelativePath(req.Path),
		},
	}
	// FIXME add a dedicated property on the CS3 InitiateFileDownloadRequest message
	// well the gateway never forwards the initiate request to the storageprovider, so we have to send it in the actual GET request
	// if len(req.IfNoneMatch) > 0 {
	//   dreq.Opaque = utils.AppendPlainToOpaque(dreq.Opaque, "if-none-match", strings.Join(req.IfNoneMatch, ","))
	// }

	// InitiateFileDownload does not read the file. It only returns the
	// download URL. So an error from this call is a connection error, for
	// example Unavailable from a dead replica. The error does not mean that
	// the file is missing. For a missing file, the GET request below gets
	// HTTP 404, and that gives NotFound.
	//
	// Return the error as it is. Do not change it to NotFound. A caller that
	// gets NotFound continues as if the file does not exist. For example, the
	// settings service then replaces the role of a user with the default role.
	res, err := client.InitiateFileDownload(ctx, &dreq)
	if err != nil {
		return nil, err
	}
	if res.GetStatus().GetCode() != rpc.Code_CODE_OK {
		return nil, errtypes.NewErrtypeFromStatus(res.GetStatus())
	}

	var endpoint string

	for _, proto := range res.GetProtocols() {
		if proto.Protocol == "spaces" {
			endpoint = proto.GetDownloadEndpoint()
			break
		}
	}
	if endpoint == "" {
		return nil, errors.New("metadata storage doesn't support the spaces download protocol")
	}

	hreq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	for _, etag := range req.IfNoneMatch {
		hreq.Header.Add(net.HeaderIfNoneMatch, etag)
	}

	md, _ := metadata.FromOutgoingContext(ctx)
	hreq.Header.Add(ctxpkg.TokenHeader, md.Get(ctxpkg.TokenHeader)[0])
	// Send the trace context. Then the work of the data server is in the trace of the request.
	rtrace.Propagator.Inject(ctx, propagation.HeaderCarrier(hreq.Header))
	resp, err := cs3.dataGatewayClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	httpStatus = resp.StatusCode

	dres := DownloadResponse{}

	dres.Etag = resp.Header.Get("etag")
	dres.Etag = resp.Header.Get("oc-etag") // takes precedence

	if err := errtypes.NewErrtypeFromHTTPStatusCode(resp.StatusCode, hreq.URL.Path); err != nil {
		return nil, err
	}

	dres.Mtime, err = time.Parse(time.RFC1123Z, resp.Header.Get("last-modified"))
	if err != nil {
		return nil, err
	}

	dres.Content, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return &dres, nil
}

// Delete deletes a path
func (cs3 *CS3) Delete(ctx context.Context, path string) (err error) {
	ctx, span := startSpan(ctx, "Delete")
	defer span.End()
	defer func() { endMetadataSpan(span, path, 0, -1, err) }()

	client, err := cs3.providerClient()
	if err != nil {
		return err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return err
	}

	res, err := client.Delete(ctx, &provider.DeleteRequest{
		Ref: &provider.Reference{
			ResourceId: cs3.SpaceRoot,
			Path:       utils.MakeRelativePath(path),
		},
	})
	if err != nil {
		return err
	}
	if res.Status.Code != rpc.Code_CODE_OK {
		return errtypes.NewErrtypeFromStatus(res.Status)
	}

	return nil
}

// ReadDir returns the entries in a given directory
func (cs3 *CS3) ReadDir(ctx context.Context, path string) ([]string, error) {
	ctx, span := startSpan(ctx, "ReadDir")
	defer span.End()

	infos, err := cs3.ListDir(ctx, path)
	if err != nil {
		return nil, err
	}

	entries := []string{}
	for _, ri := range infos {
		entries = append(entries, ri.Path)
	}
	return entries, nil
}

// ListDir returns a list of ResourceInfos for the entries in a given directory
func (cs3 *CS3) ListDir(ctx context.Context, path string) (_ []*provider.ResourceInfo, err error) {
	ctx, span := startSpan(ctx, "ListDir")
	defer span.End()
	defer func() { endMetadataSpan(span, path, 0, -1, err) }()

	client, err := cs3.providerClient()
	if err != nil {
		return nil, err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return nil, err
	}

	relPath := utils.MakeRelativePath(path)
	res, err := client.ListContainer(ctx, &provider.ListContainerRequest{
		Ref: &provider.Reference{
			ResourceId: cs3.SpaceRoot,
			Path:       relPath,
		},
	})

	if err != nil {
		return nil, err
	}
	if res.Status.Code != rpc.Code_CODE_OK {
		return nil, errtypes.NewErrtypeFromStatus(res.Status)
	}

	return res.Infos, nil
}

// MakeDirIfNotExist will create a root node in the metadata storage. Requires an authenticated context.
func (cs3 *CS3) MakeDirIfNotExist(ctx context.Context, folder string) (err error) {
	ctx, span := startSpan(ctx, "MakeDirIfNotExist")
	defer span.End()
	defer func() { endMetadataSpan(span, folder, 0, -1, err) }()

	client, err := cs3.providerClient()
	if err != nil {
		return err
	}
	ctx, err = cs3.getAuthContext(ctx)
	if err != nil {
		return err
	}

	var rootPathRef = &provider.Reference{
		ResourceId: cs3.SpaceRoot,
		Path:       utils.MakeRelativePath(folder),
	}

	resp, err := client.CreateContainer(ctx, &provider.CreateContainerRequest{
		Ref: rootPathRef,
	})
	switch {
	case err != nil:
		return err
	case resp.Status.Code == rpc.Code_CODE_OK:
		// nothing to do in this case
		return nil
	case resp.Status.Code == rpc.Code_CODE_ALREADY_EXISTS:
		// nothing to do in this case
		return nil
	default:
		return errtypes.NewErrtypeFromStatus(resp.Status)
	}
}

// CreateSymlink creates a symlink
func (cs3 *CS3) CreateSymlink(ctx context.Context, oldname, newname string) error {
	ctx, span := startSpan(ctx, "CreateSymlink")
	defer span.End()

	if _, err := cs3.ResolveSymlink(ctx, newname); err == nil {
		return os.ErrExist
	}

	return cs3.SimpleUpload(ctx, newname, []byte(oldname))
}

// ResolveSymlink resolves a symlink
func (cs3 *CS3) ResolveSymlink(ctx context.Context, name string) (string, error) {
	ctx, span := startSpan(ctx, "ResolveSymlink")
	defer span.End()

	b, err := cs3.SimpleDownload(ctx, name)
	if err != nil {
		if errors.Is(err, errtypes.NotFound("")) {
			return "", os.ErrNotExist
		}
		return "", err
	}

	return string(b), err
}

func (cs3 *CS3) providerClient() (provider.ProviderAPIClient, error) {
	return pool.GetStorageProviderServiceClient(cs3.providerAddr)
}

func (cs3 *CS3) spacesClient() (provider.SpacesAPIClient, error) {
	return pool.GetSpacesProviderServiceClient(cs3.providerAddr)
}

func (cs3 *CS3) getAuthContext(ctx context.Context) (context.Context, error) {
	if !cs3.useSystemUser {
		return ctx, nil
	}

	// Start a new context. Then the x-access-token of the caller is not in the
	// outgoing metadata. The new context has no deadline and no cancel of the
	// caller. Copy only the span and the baggage of the caller. Then all calls
	// of the operation stay in the trace of the request.
	authCtx := trace.ContextWithSpan(context.Background(), trace.SpanFromContext(ctx))
	authCtx = baggage.ContextWithBaggage(authCtx, baggage.FromContext(ctx))
	authCtx = rtrace.ContextSetTracerProvider(authCtx, rtrace.ContextGetTracerProviderOrDefault(ctx))
	authCtx = ctxpkg.ContextSetUser(authCtx, cs3.serviceUser)

	// The span covers only the Authenticate call. The returned context does
	// not hold it, so the next calls are children of the span of the caller.
	spanCtx, span := startSpan(authCtx, "getAuthContext")
	defer span.End()

	selector, err := pool.GatewaySelector(cs3.gatewayAddr)
	if err != nil {
		return nil, err
	}
	client, err := selector.Next()
	if err != nil {
		return nil, err
	}

	// The Authenticate call gets a context of its own: a gateway that is stuck
	// on a downstream storage provider accepts the connection and then never
	// answers, and there is nothing else that would ever end the wait. The
	// deadline cannot live on authCtx, because the caller runs its own RPC on
	// the context we return here.
	timeout := cs3.authRPCTimeout
	if timeout <= 0 {
		timeout = defaultAuthRPCTimeout
	}
	rpcCtx, cancel := context.WithTimeout(spanCtx, timeout)
	defer cancel()

	start := time.Now()
	authRes, err := client.Authenticate(rpcCtx, &gateway.AuthenticateRequest{
		Type:         "machine",
		ClientId:     "userid:" + cs3.serviceUser.Id.OpaqueId,
		ClientSecret: cs3.machineAuthAPIKey,
	})
	if err != nil {
		// this used to fail silently by never returning at all, so make sure
		// an exhausted deadline is visible at production log level
		appctx.GetLogger(ctx).Warn().Err(err).Str("gateway_addr", cs3.gatewayAddr).
			Dur("elapsed", time.Since(start)).Dur("timeout", timeout).Msg("cs3: machine authentication failed")
		return nil, err
	}
	if authRes.GetStatus().GetCode() != rpc.Code_CODE_OK {
		return nil, errtypes.NewErrtypeFromStatus(authRes.GetStatus())
	}
	authCtx = metadata.AppendToOutgoingContext(authCtx, ctxpkg.TokenHeader, authRes.Token)
	return authCtx, nil
}
