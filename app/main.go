package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "numeric loopback IP and port")
	unixPath := flag.String("listen-unix", "", "protected owner-only Linux Unix socket, mutually exclusive with -listen")
	unixAuthority := flag.String("unix-https-authority", "", "exact lowercase HTTPS authority for protected Unix ingress")
	state := flag.String("state", "", "directory for private application files")
	initOnly := flag.Bool("init", false, "apply migrations and exit")
	checkOnly := flag.Bool("check", false, "verify the PostgreSQL schema and existing state without changing them")
	admin := flag.String("create-admin", "", "create a local platform administrator (password on stdin)")
	enableDelivery := flag.Bool("enable-delivery", false, "enable the prototype delivery inbox for permitted staff")
	enablePOS := flag.Bool("enable-pos", false, "enable the prototype POS for permitted staff")
	enableMCP := flag.Bool("enable-mcp", false, "allow scoped staff read-only MCP access")
	enableWikiProposals := flag.Bool("enable-mcp-wiki-proposals", false, "allow administrators to propose new wiki articles for browser approval")
	enableDocumentProposals := flag.Bool("enable-mcp-document-proposals", false, "allow staff to propose document drafts for browser approval")
	enableWikiEditProposals := flag.Bool("enable-mcp-wiki-edit-proposals", false, "allow administrators to propose wiki edits for browser approval")
	enableStockReceiptProposals := flag.Bool("enable-mcp-stock-receipt-proposals", false, "allow staff to propose stock receipts for browser approval")
	enableProposalSessions := flag.Bool("enable-mcp-proposal-sessions", false, "allow selected chat proposals for browser approval")
	enableStock := flag.Bool("enable-stock", false, "enable optional finished-item stock for the prototype POS")
	publicReservations := flag.Bool("public-reservations", false, "enable the guest reservation routes for explicitly enabled venues")
	flag.Parse()
	if *enableProposalSessions && !*enablePOS {
		log.Fatal("-enable-mcp-proposal-sessions requires -enable-pos")
	}
	if *enableStockReceiptProposals && (!*enablePOS || !*enableStock) {
		log.Fatal("-enable-mcp-stock-receipt-proposals requires -enable-pos and -enable-stock")
	}
	if *enableStock && !*enablePOS {
		log.Fatal("-enable-stock requires -enable-pos")
	}
	explicitTCP, unixMode := false, false
	flag.Visit(func(f *flag.Flag) {
		explicitTCP = explicitTCP || f.Name == "listen"
		unixMode = unixMode || f.Name == "listen-unix" || f.Name == "unix-https-authority"
	})
	modes := 0
	for _, enabled := range []bool{*initOnly, *checkOnly, *admin != ""} {
		if enabled {
			modes++
		}
	}
	if *state == "" || flag.NArg() != 0 || modes > 1 || modes != 0 && (explicitTCP || unixMode || *publicReservations || *enablePOS || *enableDelivery || *enableMCP || *enableWikiProposals || *enableDocumentProposals || *enableWikiEditProposals || *enableStockReceiptProposals || *enableProposalSessions) {
		log.Fatal("provide -state; choose at most one of -init, -check and -create-admin without server flags")
	}
	var addr netip.AddrPort
	if unixMode {
		if explicitTCP || !validUnixSocketPath(*unixPath) || !validUnixAuthority(*unixAuthority) {
			log.Fatal("provide a protected -listen-unix path and -unix-https-authority, with no other listener or mode")
		}
	} else {
		var err error
		addr, err = netip.ParseAddrPort(*listen)
		if err != nil || addr.Addr().Zone() != "" || !addr.Addr().IsLoopback() {
			log.Fatal("-listen must use a numeric loopback IP and port")
		}
	}
	// Resolve and reserve the exact protected listener before opening (or
	// migrating) state. An invalid, unsafe, or occupied path cannot cause
	// any state writes. The verified socket is deliberately retained if a
	// later startup step fails; never unlink a path on fatal startup.
	var ingress *unixIngress
	if unixMode {
		var err error
		ingress, err = listenProtectedUnix(*unixPath)
		if err != nil {
			log.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	db, err := connectPostgres(ctx, os.Getenv("GOMAR_POSTGRES_DSN"))
	if err == nil {
		if *initOnly {
			err = preparePostgresState(*state)
		} else {
			err = verifyPostgresState(*state)
		}
	}
	if err == nil {
		if *initOnly {
			err = migratePostgres(ctx, db)
		} else {
			err = verifyPostgresMigrations(ctx, db)
		}
	}
	cancel()
	if err != nil {
		log.Fatalf("open foundation state: %v", err)
	}
	defer db.Close()
	if *initOnly || *checkOnly {
		return
	}
	if *admin != "" {
		password, err := bufio.NewReaderSize(os.Stdin, 4096).ReadSlice('\n')
		if err != nil || len(strings.TrimSpace(*admin)) == 0 || len(strings.TrimRight(string(password), "\r\n")) < 8 {
			log.Fatal("provide a username and a password of at least eight bytes followed by a newline")
		}
		encoded := encodePassword(strings.TrimRight(string(password), "\r\n"))
		if _, err := db.ExecContext(context.Background(), `INSERT INTO users(username,password,active,staff,superuser)
			VALUES ($1, $2, TRUE, TRUE, TRUE)`, *admin, encoded); err != nil {
			log.Fatal("could not create local administrator")
		}
		return
	}
	privateRoot, err := os.OpenRoot(filepath.Join(*state, "private"))
	if err != nil {
		log.Fatalf("open private storage: %v", err)
	}
	defer privateRoot.Close()
	var listener net.Listener
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	application := &app{db: db, privateRoot: privateRoot, publicReservations: *publicReservations, enablePOS: *enablePOS,
		enableDelivery: *enableDelivery, enableStock: *enableStock, enableMCP: *enableMCP, enableMCPWikiProposals: *enableWikiProposals, enableMCPDocumentProposals: *enableDocumentProposals, enableMCPWikiEditProposals: *enableWikiEditProposals, enableMCPStockReceiptProposals: *enableStockReceiptProposals, enableMCPProposalSessions: *enableProposalSessions}
	if ingress != nil {
		listener = ingress.listener
		server.DisableGeneralOptionsHandler = true
		server.BaseContext = ingress.baseContext
		server.ConnContext = ingress.connContext
		application.host = *unixAuthority
		server.Handler = ingress.wrapHTTPS(*unixAuthority, application.routes())
		fmt.Printf("unix://%s\n", *unixPath)
	} else {
		listener, err = net.Listen("tcp", addr.String())
		if err != nil {
			log.Fatal(err)
		}
		application.host = listener.Addr().String()
		server.Handler = application.routes()
		fmt.Printf("http://%s\n", listener.Addr())
	}
	pruneContext, stopPruning := context.WithCancel(context.Background())
	defer stopPruning()
	go application.pruneWikiProposalsLoop(pruneContext)
	log.Fatal(server.Serve(listener))
}
