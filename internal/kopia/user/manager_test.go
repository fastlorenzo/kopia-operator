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

	Context("buildCreateUserCommand", func() {
		It("should pass credentials as positional arguments, not in the script", func() {
			cmd := buildCreateUserCommand("ns-pvc@host", "secret123", "fp456")
			Expect(cmd[0]).To(Equal("/bin/sh"))
			Expect(cmd[1]).To(Equal("-c"))
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("ns-pvc@host"))
			Expect(script).NotTo(ContainSubstring("secret123"))
			Expect(script).NotTo(ContainSubstring("fp456"))
			Expect(cmd[3]).To(Equal("_"))
			Expect(cmd[4]).To(Equal("ns-pvc@host"))
			Expect(cmd[5]).To(Equal("secret123"))
			Expect(cmd[6]).To(Equal("fp456"))
		})

		It("should safely handle adversarial input with shell metacharacters", func() {
			adversarial := "'; rm -rf / #"
			cmd := buildCreateUserCommand(adversarial, adversarial, adversarial)
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring(adversarial))
			Expect(cmd[4]).To(Equal(adversarial))
			Expect(cmd[5]).To(Equal(adversarial))
			Expect(cmd[6]).To(Equal(adversarial))
		})

		It("should handle passwords containing single quotes and backticks", func() {
			cmd := buildCreateUserCommand("user@host", "p'a`ss$word", "fp")
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("p'a`ss$word"))
			Expect(cmd[5]).To(Equal("p'a`ss$word"))
		})
	})

	Context("buildDeleteUserCommand", func() {
		It("should pass username as a positional argument", func() {
			cmd := buildDeleteUserCommand("test@host")
			Expect(cmd[0]).To(Equal("/bin/sh"))
			Expect(cmd[1]).To(Equal("-c"))
			Expect(cmd[3]).To(Equal("_"))
			Expect(cmd[4]).To(Equal("test@host"))
		})

		It("should not interpolate username into the shell script", func() {
			cmd := buildDeleteUserCommand("'; drop table users; --")
			script := cmd[2]
			Expect(script).NotTo(ContainSubstring("'; drop table users; --"))
			Expect(cmd[4]).To(Equal("'; drop table users; --"))
		})
	})
})
