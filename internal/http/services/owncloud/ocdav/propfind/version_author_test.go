package propfind_test

import (
	"context"
	"strings"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocdav/net"
	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocdav/propfind"
	"github.com/owncloud/reva/v2/pkg/utils"
)

var _ = Describe("version author properties", func() {
	body := `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop>
<oc:meta-version-edited-by /><oc:meta-version-edited-by-name />
</d:prop></d:propfind>`

	respond := func(md *provider.ResourceInfo) string {
		pf, _, err := propfind.ReadPropfind(strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		ctx := context.WithValue(context.Background(), net.CtxKeyBaseURI, "/remote.php/dav/meta/id/versions")
		b, err := propfind.MultistatusResponse(ctx, &pf, []*provider.ResourceInfo{md}, "", "", nil, false)
		Expect(err).ToNot(HaveOccurred())
		return string(b)
	}
	version := func(o interface{}) *provider.ResourceInfo {
		md := &provider.ResourceInfo{Type: provider.ResourceType_RESOURCE_TYPE_FILE, Path: "v/key"}
		if o != nil {
			md.Opaque = utils.AppendPlainToOpaque(nil, propfind.OpaqueVersionAuthorUsername, "carol")
			md.Opaque = utils.AppendPlainToOpaque(md.Opaque, propfind.OpaqueVersionAuthorDisplayName, "Carol King")
		}
		return md
	}

	It("reports username and display name of the author", func() {
		out := respond(version(true))
		Expect(out).To(ContainSubstring("<oc:meta-version-edited-by>carol</oc:meta-version-edited-by>"))
		Expect(out).To(ContainSubstring("<oc:meta-version-edited-by-name>Carol King</oc:meta-version-edited-by-name>"))
	})

	It("answers 404 for the properties of a version without a recorded author", func() {
		out := respond(version(nil))
		Expect(out).To(ContainSubstring("HTTP/1.1 404 Not Found"))
		Expect(out).ToNot(ContainSubstring("carol"))
	})
})
