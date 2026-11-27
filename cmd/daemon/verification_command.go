package main

import (
	"context"
	"errors"
	"io"
	"log"

	"github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/tenant"
)

type verificationDBConfig struct {
	MasterDSN         string
	MasterDatabase    string
	RuntimeBaseDSN    string
	OnboardingBaseDSN string
	EncryptionKey     string
	TenantDBPrefix    string
	TenantNamePrefix  string
}

func dispatchVerificationFlags(
	verifyDB bool,
	jsonOutput bool,
	runVerification func() int,
) (bool, int, error) {
	if !verifyDB && !jsonOutput {
		return false, 0, nil
	}
	if !verifyDB || !jsonOutput {
		return true, 2, errors.New("verification mode requires --verify-db and --json together")
	}
	if runVerification == nil {
		return true, 2, errors.New("database verification runner is not configured")
	}
	return true, runVerification(), nil
}

func runDatabaseVerification(w io.Writer, cfg verificationDBConfig) int {
	checks := make([]verificationCheck, 0)
	exitCode := 0

	var encKey *secutil.Key
	if parsedKey, err := secutil.NewKey(cfg.EncryptionKey); err != nil {
		checks = append(checks, verificationFailure(
			"configuration.encryption_key",
			verificationTarget{Operation: "validate_encryption_key"},
			verificationErrorConfiguration,
			nil,
		))
		exitCode = 2
	} else {
		encKey = parsedKey
	}

	resolvedDBPrefix, prefixErr := tenant.ResolveDBPrefix(cfg.TenantDBPrefix, cfg.TenantNamePrefix)
	if prefixErr != nil {
		checks = append(checks, verificationFailure(
			"configuration.tenant_db_prefix",
			verificationTarget{Operation: "validate_tenant_db_prefix"},
			verificationErrorConfiguration,
			nil,
		))
		exitCode = 2
	}

	masterValid := validVerificationMasterDSN(cfg.MasterDSN, cfg.MasterDatabase)
	if !validVerificationBaseDSN(cfg.RuntimeBaseDSN) {
		checks = append(checks, verificationFailure(
			"configuration.runtime_dsn",
			verificationTarget{
				AccountRole: "runtime",
				Operation:   "validate_runtime_dsn",
			},
			verificationErrorConfiguration,
			nil,
		))
		exitCode = 2
	}
	if cfg.OnboardingBaseDSN != "" && !validVerificationBaseDSN(cfg.OnboardingBaseDSN) {
		checks = append(checks, verificationFailure(
			"configuration.onboarding_dsn",
			verificationTarget{
				AccountRole: "onboarding",
				Operation:   "validate_onboarding_dsn",
			},
			verificationErrorConfiguration,
			nil,
		))
		exitCode = 2
	}
	if !masterValid {
		target := verificationTarget{
			AccountRole: "runtime",
			Database:    cfg.MasterDatabase,
			Operation:   "validate_master_dsn",
		}
		checks = append(checks, verificationFailure(
			"configuration.master_dsn",
			target,
			verificationErrorConfiguration,
			nil,
		))
		checks = append(checks, verificationFailure(
			"master.connection",
			verificationTarget{
				AccountRole: "runtime",
				Database:    cfg.MasterDatabase,
				Operation:   "connect",
			},
			verificationErrorConfiguration,
			nil,
		))
		checks = append(checks, newVerificationNotVerified(
			"master.registry",
			verificationTarget{
				AccountRole: "runtime",
				Database:    cfg.MasterDatabase,
				Operation:   "load_enabled_tenants",
			},
			verificationErrorConfiguration,
		))
		exitCode = 2
	} else if prefixErr != nil {
		checks = append(checks, newVerificationNotVerified(
			"master.registry",
			verificationTarget{
				AccountRole: "runtime",
				Database:    cfg.MasterDatabase,
				Operation:   "load_enabled_tenants",
			},
			verificationErrorConfiguration,
		))
	} else {
		checks = append(checks, verifyDatabase(
			context.Background(),
			cfg.MasterDSN,
			cfg.MasterDatabase,
			cfg.RuntimeBaseDSN,
			cfg.OnboardingBaseDSN,
			encKey,
			defaultVerificationDBOpsWithPrefix(resolvedDBPrefix),
		)...)
	}

	if err := writeVerificationReport(w, checks); err != nil {
		log.Print("database verification report could not be written")
		return 2
	}
	report, err := buildVerificationReport(checks)
	if err != nil {
		log.Print("database verification report could not be built")
		return 2
	}
	if exitCode != 0 {
		return exitCode
	}
	if report.OverallStatus != verificationPass {
		return 1
	}
	return 0
}

func validVerificationMasterDSN(dsn, database string) bool {
	if dsn == "" || database == "" {
		return false
	}
	cfg, err := mysql.ParseDSN(dsn)
	return err == nil && cfg.DBName == database
}

func validVerificationBaseDSN(dsn string) bool {
	if dsn == "" {
		return false
	}
	cfg, err := mysql.ParseDSN(dsn)
	return err == nil && cfg.DBName == ""
}
