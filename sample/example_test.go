package sample_test

import (
	"fmt"
	"os"

	"github.com/zatca-go/zatca/sample"
)

func Example() {
	// Set required env vars (normally done in .env or docker-compose)
	os.Setenv("DBNAME", "dev_ifritah")
	os.Setenv("NATS_URL", "nats://localhost:4222")

	// Create publisher — call once at app startup
	pub, err := sample.NewZATCAPublisher()
	if err != nil {
		fmt.Println("connect error:", err)
		return
	}
	defer pub.Close()

	// ── After saving a new bill ──────────────────────────────────
	// Your backend saves the bill and gets back billID and branchID:
	//   billID   := result.LastInsertId()   // e.g. 55
	//   branchID := currentUser.BranchID    // e.g. 42
	if err := pub.SubmitBill(55, 42); err != nil {
		fmt.Println("submit bill error:", err)
	}

	// ── After saving a credit note ───────────────────────────────
	// creditNoteID := result.LastInsertId()  // e.g. 10
	// branchID comes from the original bill's branch
	if err := pub.SubmitCredit(10, 42); err != nil {
		fmt.Println("submit credit error:", err)
	}

	// ── After saving a debit note ────────────────────────────────
	if err := pub.SubmitDebit(7, 42); err != nil {
		fmt.Println("submit debit error:", err)
	}

	// ── One-time: onboard a branch with OTP from Fatoora portal ──
	// Get OTP from https://fatoora.zatca.gov.sa
	if err := pub.OnboardBranch(42, "123456"); err != nil {
		fmt.Println("onboard error:", err)
	}
}
