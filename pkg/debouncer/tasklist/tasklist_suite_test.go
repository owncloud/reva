package tasklist_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTaskList(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Debouncer task list suite")
}
