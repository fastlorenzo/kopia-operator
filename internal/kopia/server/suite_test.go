package server

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestServerManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Server Manager Suite")
}
