package user

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fastlorenzo/kopia-operator/internal/kopia"
)

var _ = Describe("User Manager Helpers", func() {
	Context("generateSecurePassword", func() {
		It("should generate a password of the correct length", func() {
			pw, err := generateSecurePassword(32)
			Expect(err).NotTo(HaveOccurred())
			Expect(pw).To(HaveLen(32))
		})

		It("should generate different passwords each time", func() {
			pw1, _ := generateSecurePassword(32)
			pw2, _ := generateSecurePassword(32)
			Expect(pw1).NotTo(Equal(pw2))
		})
	})

	Context("ServerNotReadyError", func() {
		It("should implement error interface", func() {
			err := &kopia.ServerNotReadyError{Message: "deployment not found"}
			Expect(err.Error()).To(ContainSubstring("deployment not found"))
		})
	})
})
