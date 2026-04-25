package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/go-sql-driver/mysql"

	"github.com/zatca-go/zatca/config"
	"github.com/zatca-go/zatca/dbutil"
	"github.com/zatca-go/zatca/secutil"
	"github.com/zatca-go/zatca/storeconfig"
)

func main() {
	dsn := flag.String("dsn", "", "Full MySQL DSN (alternative to individual flags)")
	dbUser := flag.String("db-user", "", "Database username")
	dbPass := flag.String("db-pass", "", "Database password (safe for special characters)")
	dbHost := flag.String("db-host", "", "Database host")
	dbPort := flag.String("db-port", "3306", "Database port")
	dbName := flag.String("db-name", "", "Database name")
	zatcaEnv := flag.String("zatca-env", "production", "ZATCA environment: sandbox, simulation, production")
	ping := flag.Bool("ping", false, "Ping ZATCA API to verify connectivity (slower)")
	asJSON := flag.Bool("json", false, "Output as JSON")
	flag.Parse()

	if *dsn == "" {
		if envDSN := os.Getenv("DSN"); envDSN != "" {
			*dsn = envDSN
		} else {
			if *dbUser == "" {
				*dbUser = os.Getenv("DBUSER")
			}
			if *dbPass == "" {
				*dbPass = os.Getenv("PASSWORD")
			}
			if *dbHost == "" {
				*dbHost = os.Getenv("HOST")
			}
			if p := os.Getenv("PORT"); p != "" && *dbPort == "3306" {
				*dbPort = p
			}
			if *dbName == "" {
				*dbName = os.Getenv("DBNAME")
			}
			resolved, err := dbutil.ResolveDSN("", *dbUser, *dbPass, *dbHost, *dbPort, *dbName)
			if err != nil {
				log.Fatalf("DSN required: use --dsn or --db-user/--db-pass/--db-host/--db-name: %v", err)
			}
			*dsn = resolved
		}
	}

	baseCfg := &config.Config{Env: config.Environment(*zatcaEnv)}

	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	statuses, err := storeconfig.LoadAllBranchStatuses(db)
	if err != nil {
		log.Fatalf("load branch statuses: %v", err)
	}

	if *ping {
		encKeyStr := os.Getenv("ENCRYPT_KEY")
		if encKeyStr == "" {
			log.Fatal("ENCRYPT_KEY environment variable is required for --ping")
		}
		encKey, err := secutil.NewKey(encKeyStr)
		if err != nil {
			log.Fatalf("invalid ENCRYPT_KEY: %v", err)
		}
		for i := range statuses {
			storeconfig.CheckZATCAConnection(&statuses[i], baseCfg, db, encKey)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(statuses); err != nil {
			log.Fatalf("encode json: %v", err)
		}
		return
	}

	// Table output
	fmt.Printf("%-8s %-8s %-25s %-25s %-20s %-16s %s\n", "BRANCH", "STORE", "BRANCH NAME", "COMPANY", "VAT", "STATUS", "MESSAGE")
	fmt.Println("-------- -------- ------------------------- ------------------------- -------------------- ---------------- -------------------------")
	for _, s := range statuses {
		fmt.Printf("%-8d %-8d %-25s %-25s %-20s %-16s %s\n", s.BranchID, s.StoreID, truncate(s.BranchName, 25), truncate(s.Company, 25), s.VAT, s.Status, s.Message)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-2] + ".."
}
