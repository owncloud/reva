package providercache

import (
	"encoding/json"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
)

// legacyUnmarshalShares is the decoder that the code used before the one-pass
// decoder: three passes for each share. It is the reference of the differential
// tests. Do not change it.
func legacyUnmarshalShares(data []byte) (*Shares, error) {
	tmp := struct {
		Shares map[string]json.RawMessage
	}{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, err
	}
	s := &Shares{Shares: make(map[string]*collaboration.Share, len(tmp.Shares))}
	for id, genericShare := range tmp.Shares {
		userShare := &collaboration.Share{
			Grantee: &provider.Grantee{Id: &provider.Grantee_UserId{}},
		}
		err := json.Unmarshal(genericShare, userShare)
		if err == nil && userShare.Grantee.Type == provider.GranteeType_GRANTEE_TYPE_USER {
			s.Shares[id] = userShare
			continue
		}
		groupShare := &collaboration.Share{
			Grantee: &provider.Grantee{Id: &provider.Grantee_GroupId{}},
		}
		err = json.Unmarshal(genericShare, groupShare)
		if err == nil && groupShare.Grantee.Type == provider.GranteeType_GRANTEE_TYPE_GROUP {
			s.Shares[id] = groupShare
			continue
		}
		invalidShare := &collaboration.Share{}
		err = json.Unmarshal(genericShare, invalidShare)
		if err == nil {
			s.Shares[id] = invalidShare
			continue
		}
		return nil, err
	}
	return s, nil
}
