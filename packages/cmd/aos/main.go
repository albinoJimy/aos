package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	identity "github.com/aos-ref/platform/identity"
)

// ErrProductionNeedsHardenedIdentity — sob AOS_MODE=production o nó RECUSA arrancar no
// modo de referência (autoridade co-localizada): exige o trust-anchor-only via
// AOS_ISSUER_PUBKEY, para que nenhuma chave de assinatura viva no processo do nó. Um
// operador não pode, por engano, correr o arranque de referência como fronteira de
// produção endurecida (fail-closed).
var ErrProductionNeedsHardenedIdentity = errors.New("aos: AOS_MODE=production exige AOS_ISSUER_PUBKEY (trust-anchor-only) — a autoridade co-localizada de referencia nao e uma fronteira de producao")

// ErrBadIssuerPubKey — AOS_ISSUER_PUBKEY presente mas não é uma pubkey ed25519 válida
// (32 bytes em hex). Fail-closed: um anchor malformado nunca compõe o verifier.
var ErrBadIssuerPubKey = errors.New("aos: AOS_ISSUER_PUBKEY invalida (esperado 64 hex chars = 32 bytes ed25519)")

// main arranca o nó `aos` de PRODUÇÃO a partir de config de ambiente e declara o modo
// de identidade em vigor. O loop de serviço long-running (hospedar N runs, shutdown
// gracioso) é AOS-164; aqui prova-se o BOOTSTRAP fail-closed e a declaração do modo
// REAL. Sem segredos em código: o IssuerID e os humanos vêm do ambiente; a chave de
// assinatura de referência é gerada por CSPRNG (a real vem de vault — AOS-170/EPIC-06).
func main() {
	// A CLI (AOS-165) despacha os subcomandos: `serve` (ou sem args) arranca o nó via [run];
	// `run`/`observe`/`steer`/`pause` são clientes HTTP da API (AOS-166).
	if err := dispatch(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos: %v\n", err)
		os.Exit(1)
	}
}

// run compõe e valida o nó, escrevendo o banner em w. Extraída de main para ser
// exercitável sem processo. NÃO entra no loop de serviço (AOS-164): o objectivo de
// AOS-163 é o arranque de produção fail-closed com identidade real declarada.
func run(w io.Writer) error {
	ctx := context.Background()

	issuerID := envOr("AOS_ISSUER_ID", "iss:aos-node")
	humans := splitCSV(envOr("AOS_HUMANS", "operator"))
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")

	// Trust-anchor-only ENDURECIDO: se AOS_ISSUER_PUBKEY estiver presente, o nó recebe só
	// a pubkey do issuer (a autoridade/chave vivem FORA do processo). É o modo de fronteira
	// de produção. Fail-closed: uma pubkey malformada aborta.
	var issuerPub ed25519.PublicKey
	if raw := strings.TrimSpace(os.Getenv("AOS_ISSUER_PUBKEY")); raw != "" {
		pub, err := parseEd25519PubHex(raw)
		if err != nil {
			return err
		}
		issuerPub = pub
	}

	// FAIL-CLOSED de produção: AOS_MODE=production recusa o modo de referência (autoridade
	// co-localizada). Um operador não pode confundir o arranque de referência com uma
	// fronteira de produção endurecida — exige-se o trust-anchor-only.
	if production && issuerPub == nil {
		return ErrProductionNeedsHardenedIdentity
	}

	// Config MÍNIMA. Numa implantação real, IssuerSigningKey e as pubkeys dos operadores/
	// aprovadores vêm de config/vault (nunca do código). No modo de referência (sem
	// AOS_ISSUER_PUBKEY) a chave é gerada por CSPRNG e a autoridade co-localizada
	// encapsula-a — nunca é impressa nem entra na cadeia de segurança; o banner AVISA que
	// é referência. No modo endurecido (AOS_ISSUER_PUBKEY presente) NENHUMA chave de
	// assinatura entra no processo.
	cfg := Config{
		IssuerID:     issuerID,
		Humans:       humans,
		IssuerPubKey: issuerPub, // nil ⇒ referência; presente ⇒ trust-anchor-only endurecido
		IssuerClasses: map[string]identity.ClassPolicy{
			"researcher": {TTL: 15 * time.Minute, Scope: []string{"cap:doc.read"}},
		},
		// Operators vazio ⇒ default-deny do canal de controlo até serem configuradas
		// pubkeys de operador (o steer anónimo é recusado — a inércia do D4 não protege
		// pause/steer).
	}

	node, err := Bootstrap(ctx, cfg, w)
	if err != nil {
		return err
	}
	defer func() { _ = node.Close() }()

	// Superfície de REDE (AOS-166). Se AOS_API_ADDR estiver definido, o nó gradua para um nó
	// OPERÁVEL de fora: levanta o loop de serviço + a API HTTP com o BIND-GUARDRAIL no
	// CAMINHO DE PRODUÇÃO (é aqui, não só nos testes, que um bind não-loopback sem
	// autenticação do canal de controlo é RECUSADO). Sem AOS_API_ADDR mantém-se o arranque
	// de bootstrap/declaração (AOS-163) sem abrir socket.
	apiAddr := strings.TrimSpace(os.Getenv("AOS_API_ADDR"))
	if apiAddr == "" {
		fmt.Fprintf(w, "[aos] bootstrap concluido — no pronto (modo de identidade: %s). API HTTP nao levantada (AOS_API_ADDR vazio).\n", node.IdentityMode)
		return nil
	}

	// SIGINT/SIGTERM ⇒ paragem graciosa (drena runs em curso, liberta leases).
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(w, "[aos] bootstrap concluido — a levantar a API HTTP em %q (modo de identidade: %s)\n", apiAddr, node.IdentityMode)
	return serveAPI(sigCtx, w, node, apiAddr)
}

// serveAPI gradua o nó para a superfície de REDE (AOS-166): compõe o loop de serviço
// ([NodeService]) e o [APIServer] com o BIND-GUARDRAIL, e serve em addr. É AQUI que o
// guardrail entra no CAMINHO DE PRODUÇÃO — [APIServer.Serve] RECUSA (erro, não um log) um
// bind não-loopback enquanto o canal de controlo não estiver autenticado, pelo que a única
// barreira contra expor o canal de controlo sem authn corre no arranque REAL, não só em
// httptest. Bloqueia até ctx ser cancelado (o sinal de paragem) e então encerra
// graciosamente. Um erro SÍNCRONO de Serve (guardrail recusou / Listen falhou) retorna de
// imediato, encerrando o loop de serviço.
func serveAPI(ctx context.Context, w io.Writer, node *Node, addr string) error {
	svc, err := NewNodeService(node, WithServiceLog(w))
	if err != nil {
		return err
	}
	srv, err := NewAPIServer(svc, node, WithAPILog(w))
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)
	go func() {
		e := srv.Serve(addr) // aplica o bind-guardrail ANTES do Listen
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		serveErr <- e
	}()

	select {
	case e := <-serveErr:
		// Serve retornou cedo: o bind-guardrail RECUSOU o addr, ou o Listen falhou. O nó não
		// chega a servir — encerra o loop de serviço e propaga o erro.
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(shutCtx)
		return e
	case <-ctx.Done():
		fmt.Fprintf(w, "[aos] sinal de paragem recebido — a encerrar graciosamente (drena runs em curso, liberta leases)\n")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = svc.Shutdown(shutCtx)
		return <-serveErr
	}
}

// envOr devolve a variável de ambiente name ou def se vazia.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// parseEd25519PubHex descodifica uma pubkey ed25519 de 32 bytes em hex (64 chars).
// Fail-closed: qualquer erro de descodificação ou tamanho errado devolve
// [ErrBadIssuerPubKey] — só material público bem-formado se torna trust anchor.
func parseEd25519PubHex(raw string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadIssuerPubKey
	}
	return ed25519.PublicKey(b), nil
}

// splitCSV divide uma lista separada por vírgulas, descartando vazios e espaços.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
