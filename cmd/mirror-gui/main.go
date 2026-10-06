// Command mirror-gui serves the mirror-gui web application and its REST API.
// It also provides the catalog metadata tooling used at build time:
//
//	mirror-gui [serve]                     run the web server (default)
//	mirror-gui sync-catalogs [flags]       sync operator catalog metadata from registry.redhat.io
//	mirror-gui catalog-metadata generate   generate metadata for one extracted catalog snapshot
//	mirror-gui catalog-metadata finalize   generate metadata and the index for extracted snapshots
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nr3v0/mirror-gui/internal/catalogmeta"
	"github.com/nr3v0/mirror-gui/internal/server"
)

const usage = `Usage:
  mirror-gui [serve]
  mirror-gui sync-catalogs [--catalog-data-dir DIR] [--pull-secret FILE] [--parallel N]
  mirror-gui catalog-metadata generate --catalog-dir DIR --catalog-type TYPE --ocp-version vX.Y --operators-file FILE --dependencies-file FILE
  mirror-gui catalog-metadata finalize --catalog-data-dir DIR [--digest DIGEST] [--ocp-versions "4.16 ..."] [--catalog-types "..."]
`

func main() {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	var err error
	switch command {
	case "serve":
		err = serve()
	case "sync-catalogs":
		err = syncCatalogs(args)
	case "catalog-metadata":
		err = catalogMetadata(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func serve() error {
	cfg := server.ConfigFromEnv()
	if err := dropPrivileges(cfg); err != nil {
		return err
	}
	srv := server.New(cfg)
	srv.ClearOperationHistory()
	srv.LogStartup()
	return http.ListenAndServe(":"+cfg.Port, srv)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// syncCatalogs replaces the former bash implementation of sync-catalogs.sh and
// honours the same environment variables.
func syncCatalogs(args []string) error {
	parallel, _ := strconv.Atoi(envOr("MAX_PARALLEL_JOBS", "3"))
	fs := flag.NewFlagSet("sync-catalogs", flag.ExitOnError)
	dataDir := fs.String("catalog-data-dir", envOr("CATALOG_DATA_DIR", "./catalog-data"), "output directory (env CATALOG_DATA_DIR)")
	pullSecret := fs.String("pull-secret", envOr("PULL_SECRET_PATH", "pull-secret/pull-secret.json"),
		"registry auth file; falls back to REGISTRY_AUTH_FILE, then oc's default credentials (env PULL_SECRET_PATH)")
	fs.IntVar(&parallel, "parallel", parallel, "catalogs extracted concurrently (env MAX_PARALLEL_JOBS)")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stdout, "", 0)
	_, err := catalogmeta.Sync(ctx, catalogmeta.SyncOptions{
		DataDir:        *dataDir,
		RegistryConfig: catalogmeta.ResolveRegistryConfig(*pullSecret, os.Getenv("REGISTRY_AUTH_FILE")),
		Parallel:       parallel,
		RetryDelay:     2 * time.Second,
		Log:            func(line string) { logger.Println(line) },
	})
	return err
}

func catalogMetadata(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("catalog-metadata needs a subcommand (generate or finalize)")
	}
	switch args[0] {
	case "generate":
		return generateMetadata(args[1:])
	case "finalize":
		return finalizeMetadata(args[1:])
	}
	return fmt.Errorf("unknown catalog-metadata subcommand %q", args[0])
}

func required(fs *flag.FlagSet, names ...string) error {
	for _, name := range names {
		if fs.Lookup(name).Value.String() == "" {
			return fmt.Errorf("--%s is required", name)
		}
	}
	return nil
}

// generateMetadata is a drop-in replacement for `catalog_metadata.py generate`.
func generateMetadata(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	catalogDir := fs.String("catalog-dir", "", "path to catalog-data/<catalog>/v<version>")
	catalogType := fs.String("catalog-type", "", "catalog type, e.g. certified-operator-index")
	ocpVersion := fs.String("ocp-version", "", "catalog version, e.g. v4.20")
	operatorsFile := fs.String("operators-file", "", "output path for operators.json")
	dependenciesFile := fs.String("dependencies-file", "", "output path for dependencies.json")
	fs.Parse(args)
	if err := required(fs, "catalog-dir", "catalog-type", "ocp-version", "operators-file", "dependencies-file"); err != nil {
		return err
	}

	operators, dependencies, warnings := catalogmeta.GenerateSnapshot(*catalogDir, *catalogType, *ocpVersion)
	if err := catalogmeta.WriteJSON(*operatorsFile, operators); err != nil {
		return err
	}
	if err := catalogmeta.WriteJSON(*dependenciesFile, dependencies); err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "[WARNING]", w)
	}
	fmt.Fprintf(os.Stderr, "Generated metadata for %d operators\n", len(operators))
	fmt.Fprintf(os.Stderr, "Generated dependencies for %d operators\n", len(dependencies))
	return nil
}

// finalizeMetadata processes snapshots whose configs were extracted by other
// means (e.g. COPY --from in Dockerfile.catalog-sync) and writes catalog-index.json.
// Every requested snapshot must have a configs directory.
func finalizeMetadata(args []string) error {
	fs := flag.NewFlagSet("finalize", flag.ExitOnError)
	dataDir := fs.String("catalog-data-dir", "", "catalog data root containing <type>/v<version>/configs")
	digest := fs.String("digest", "unknown", "digest recorded in catalog-info.json")
	versions := fs.String("ocp-versions", strings.Join(catalogmeta.DefaultOCPVersions, " "), "space-separated OCP versions")
	types := fs.String("catalog-types", strings.Join(catalogmeta.DefaultCatalogTypes, " "), "space-separated catalog types")
	fs.Parse(args)
	if err := required(fs, "catalog-data-dir"); err != nil {
		return err
	}

	ocpVersions, catalogTypes := strings.Fields(*versions), strings.Fields(*types)
	logf := func(line string) { fmt.Println(line) }
	total, failed := 0, 0
	for _, version := range ocpVersions {
		for _, catalogType := range catalogTypes {
			total++
			if _, err := catalogmeta.FinalizeSnapshot(*dataDir, catalogType, version, *digest, logf); err != nil {
				fmt.Printf("ERROR: Failed to process %s v%s: %v\n", catalogType, version, err)
				failed++
			}
		}
	}
	if err := catalogmeta.WriteIndex(*dataDir, ocpVersions, catalogTypes); err != nil {
		return err
	}
	fmt.Printf("Completed: %d/%d catalogs successful, %d failed\n", total-failed, total, failed)
	if failed > 0 {
		return fmt.Errorf("%d/%d catalogs failed to process", failed, total)
	}
	return nil
}
