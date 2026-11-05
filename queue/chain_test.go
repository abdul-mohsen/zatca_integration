package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func allReservationColumns() map[string]bool {
	found := make(map[string]bool, len(chainReservationColumns))
	for _, column := range chainReservationColumns {
		found[column] = true
	}
	return found
}

func TestValidateReservationColumnsRejectsMissingColumn(t *testing.T) {
	found := allReservationColumns()
	delete(found, "reserved_icv")

	err := validateReservationColumns(found)
	if !errors.Is(err, ErrChainSchema) {
		t.Fatalf("error = %v; want ErrChainSchema", err)
	}
	for _, want := range []string{"reserved_icv", "cmd/migrate up", "00005_egs_chain_reservation.sql"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q; want it to contain %q", err, want)
		}
	}
}

func TestValidateReservationColumnsAcceptsCompleteSchema(t *testing.T) {
	if err := validateReservationColumns(allReservationColumns()); err != nil {
		t.Fatalf("validateReservationColumns = %v; want nil", err)
	}
}

func TestBookRunsSchemaPreflightBeforeUsingTheDatabase(t *testing.T) {
	chain := &SQLChain{}

	_, err := chain.Book(context.Background(), Message{})
	if !errors.Is(err, ErrChainSchema) {
		t.Fatalf("Book error = %v; want ErrChainSchema", err)
	}
	if !strings.Contains(err.Error(), "cmd/migrate up") {
		t.Fatalf("Book error = %q; want the migration command", err)
	}
}
