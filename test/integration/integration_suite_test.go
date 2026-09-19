// Package integration holds docsearch's BDD suites: behaviour checked across
// packages and against real inputs, as opposed to the unit tests beside each
// package.
package integration

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration")
}
