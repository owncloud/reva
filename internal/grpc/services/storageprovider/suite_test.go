package storageprovider

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStorageprovider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Storageprovider Suite")
}
