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

// ErrBadBoardRegions — AOS_BOARD_REGIONS presente mas com uma entrada MALFORMADA (sem '=',
// ou com board/região vazios). Fail-closed de CONFIG da soberania de leitura (AOS-172, D7):
// distingue "não configurado" (vazio ⇒ read-path legado deliberado) de "configurado mas
// inválido" (aborta). Um operador que TENCIONA ligar a soberania mas comete um typo (ex.:
// "aos-demo" sem '=') NÃO obtém um nó que serve TODAS as leituras sem authz por-chamador
// (D7) nem selo (D6) — a ambiguidade de soberania NEGA o arranque, coerente com o resto do
// fail-closed do boot (ErrProductionNeedsHardenedIdentity, ErrBadIssuerPubKey).
var ErrBadBoardRegions = errors.New("aos: AOS_BOARD_REGIONS invalida (esperado \"board=regiao,board2=regiao2\" — entrada malformada aborta em vez de degradar silenciosamente para o read-path aberto)")

// ErrProductionNeedsSovereignRead — sob AOS_MODE=production a soberania de leitura NÃO pode
// estar desligada por engano: exige-se um registo board→região NÃO-VAZIO (a par de
// ErrProductionNeedsHardenedIdentity para a identidade). Um nó de produção nunca serve o
// read-path LEGADO (sem authz por-chamador D7 nem selo D6) — fail-closed.
var ErrProductionNeedsSovereignRead = errors.New("aos: AOS_MODE=production exige AOS_BOARD_REGIONS nao-vazio (read-path soberano fail-closed) — a producao nao serve leituras sem authz por-chamador nem selo WORM")

// ErrBadDurableExecution — AOS_DURABLE_EXECUTION presente com um valor que NÃO é um
// booleano reconhecido. Fail-closed de CONFIG (AOS-191), no padrão de ErrBadBoardRegions:
// lixo NÃO é tratado como false. Um operador que escreve `AOS_DURABLE_EXECUTION=tru` (ou
// "enabled", ou "sim") TENCIONA ligar a execução durável; silenciosamente arrancar sem
// checkpointer/capturer/step-ledger dar-lhe-ia um nó que perde estado no reinício
// acreditando ele que não perde. A ambiguidade NEGA o arranque.
var ErrBadDurableExecution = errors.New("aos: AOS_DURABLE_EXECUTION invalida (aceites: 1/true/t/yes/y/on para ligar, 0/false/f/no/n/off ou vazio para desligar) — um valor nao reconhecido aborta em vez de degradar silenciosamente para execucao NAO-duravel")

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

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		return err
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

// nodeConfigFromEnv constrói a [Config] do nó a partir do AMBIENTE — a ÚNICA superfície de
// configuração do binário entregue. É a costura testável entre o processo e o
// composition-root: um campo de Config que NÃO seja escrito aqui é, na prática,
// inalcançável pelo artefacto (Config vive em `package main`, logo nem um embedder
// externo o pode preencher) — foi exactamente esse o defeito de AOS_DURABLE_EXECUTION
// (AOS-191, achado REG-01 da auditoria v4).
//
// Toda a validação FAIL-CLOSED de config vive aqui: um valor presente mas inválido ABORTA
// (ErrBadIssuerPubKey, ErrBadBoardRegions, ErrBadDurableExecution) e as exigências de
// postura de produção são impostas (ErrProductionNeedsHardenedIdentity,
// ErrProductionNeedsSovereignRead). Nada degrada em silêncio.
func nodeConfigFromEnv() (Config, error) {
	issuerID := envOr("AOS_ISSUER_ID", "iss:aos-node")
	humans := splitCSV(envOr("AOS_HUMANS", "operator"))
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("AOS_MODE")), "production")

	// SOBERANIA DE LEITURA (AOS-172, D7) — config fail-closed, sem degradação silenciosa.
	// Distingue TRÊS estados (LookupEnv, não envOr — envOr colapsaria "não definido" com
	// "definido vazio"):
	//   - NÃO DEFINIDO ⇒ default DEMO ("board:aos-demo=eu"): a soberania de leitura está LIGADA
	//     no caminho comum (o `aos serve` de referência é fail-closed por omissão);
	//   - DEFINIDO VAZIO ⇒ nil: opt-out DELIBERADO do read-path legado (aceite só fora de produção);
	//   - DEFINIDO MALFORMADO (entrada não-vazia inválida) ⇒ ABORTA (ErrBadBoardRegions): um typo
	//     (ex.: "aos-demo" sem '=') NÃO degrada em silêncio para o read-path aberto.
	rawBoardRegions, ok := os.LookupEnv("AOS_BOARD_REGIONS")
	if !ok {
		rawBoardRegions = "board:aos-demo=eu"
	}
	boardRegions, err := parseBoardRegions(rawBoardRegions)
	if err != nil {
		return Config{}, err
	}
	// FAIL-CLOSED de produção: a soberania de leitura NÃO pode estar desligada num nó de
	// produção (a par da identidade endurecida, ErrProductionNeedsHardenedIdentity). Sem
	// registo board→região não-vazio, a produção serviria o read-path LEGADO sem authz
	// por-chamador (D7) nem selo (D6) — recusa o arranque.
	if production && len(boardRegions) == 0 {
		return Config{}, ErrProductionNeedsSovereignRead
	}

	// EXECUÇÃO DURÁVEL (AOS-180) por ambiente — AOS_DURABLE_EXECUTION (AOS-191). OPT-IN:
	// ausente/vazio ⇒ false, isto é, o comportamento ACTUAL byte-a-byte (checkpointer,
	// capturer e step-ledger ficam nil e o runtime usa os defaults no-op de AOS-013).
	// Presente e verdadeiro ⇒ o nó compõe os três sobre o MESMO Event Store dos turnos e
	// sinais de controlo, e o snapshot do tool set congelado passa a persistir nele.
	//
	// Ao contrário de AOS_BOARD_REGIONS, NÃO é preciso distinguir "não definido" de
	// "definido vazio": ali o vazio era um KILL-SWITCH (desligava uma protecção LIGADA por
	// omissão); aqui o default JÁ é "desligado", pelo que vazio colapsa no default sem
	// perder informação e sem baixar a postura de segurança.
	//
	// Um valor NÃO RECONHECIDO aborta (ErrBadDurableExecution) em vez de ser tratado como
	// false — ver a justificação no erro.
	//
	// POSTURA DE PRODUÇÃO — DECISÃO EXPLÍCITA (AOS-191), não omissão. Ao contrário da
	// identidade endurecida (ErrProductionNeedsHardenedIdentity) e da soberania de leitura
	// (ErrProductionNeedsSovereignRead), AOS_MODE=production NÃO exige execução durável: um
	// nó de produção arranca sem ela. Racional: (i) não há promessa falsa — o banner declara
	// "DESLIGADA" e nada anuncia durabilidade, que é o critério que separa este caso dos dois
	// anteriores (ali o nó SERVIRIA leituras/identidade com uma postura mais fraca do que a
	// anunciada); (ii) exigi-la aqui quebraria a retro-compatibilidade que AOS-191 impõe
	// (sem a variável, o comportamento actual mantém-se) e transformaria um ticket de
	// SUPERFÍCIE DE CONFIGURAÇÃO numa mudança de postura de produção não anunciada aos
	// operadores existentes. A promoção a exigência de produção — a par das outras duas — é
	// decidida em AOS-203 (postura das variáveis de ambiente do nó), não fica em aberto sem
	// eixo. Documentado ao operador em deploy/node/README.md.
	durableExecution, err := parseDurableExecution(os.Getenv("AOS_DURABLE_EXECUTION"))
	if err != nil {
		return Config{}, err
	}
	// INTERACÇÃO COM AOS_EVENTSTORE_PATH — fail-closed SEMPRE (não só em produção). A
	// execução durável compõe-se SOBRE o Event Store: sem AOS_EVENTSTORE_PATH o nó abriria
	// um store IN-MEMORY e checkpoints/capturas/step-ledger evaporariam no reinício — o nó
	// anunciaria durabilidade e perderia tudo. A guarda canónica vive no composition-root
	// ([ErrDurableExecutionNeedsDurableSubstrate], Bootstrap (1b)) e cobre também um
	// EventStore injectado por config; aqui, na fronteira de ambiente, a condição é
	// simplesmente "AOS_DURABLE_EXECUTION=1 exige AOS_EVENTSTORE_PATH".
	eventStorePath := strings.TrimSpace(os.Getenv("AOS_EVENTSTORE_PATH"))
	if durableExecution && eventStorePath == "" {
		return Config{}, fmt.Errorf("%w (defina AOS_EVENTSTORE_PATH, ex.: /var/lib/aos/events.wal)", ErrDurableExecutionNeedsDurableSubstrate)
	}

	// Trust-anchor-only ENDURECIDO: se AOS_ISSUER_PUBKEY estiver presente, o nó recebe só
	// a pubkey do issuer (a autoridade/chave vivem FORA do processo). É o modo de fronteira
	// de produção. Fail-closed: uma pubkey malformada aborta.
	var issuerPub ed25519.PublicKey
	if raw := strings.TrimSpace(os.Getenv("AOS_ISSUER_PUBKEY")); raw != "" {
		pub, err := parseEd25519PubHex(raw)
		if err != nil {
			return Config{}, err
		}
		issuerPub = pub
	}

	// FAIL-CLOSED de produção: AOS_MODE=production recusa o modo de referência (autoridade
	// co-localizada). Um operador não pode confundir o arranque de referência com uma
	// fronteira de produção endurecida — exige-se o trust-anchor-only.
	if production && issuerPub == nil {
		return Config{}, ErrProductionNeedsHardenedIdentity
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
		// SOBERANIA DE LEITURA (AOS-172, D7). Registo board→região DEMO-GRADE self-hosted por
		// ambiente (AOS_BOARD_REGIONS = "board=regiao,board2=regiao2"), com um board demo por
		// omissão. Já validado fail-closed acima (vazio ⇒ legado; malformado ⇒ abortou). A REGRA
		// fail-closed é FIXA; o provisioning real de regiões/boards fica DEFERIDO (EPIC-09/10).
		// Ligar isto torna o read-path soberano fail-closed E o selo WORM de leitura sensível (D6)
		// — os clientes de leitura têm de declarar X-Aos-Reader/X-Aos-Board.
		BoardRegions: boardRegions,
		// SUBSTRATO DURÁVEL (AOS-170) por ambiente. OPT-IN: vazio ⇒ in-memory de referência
		// (inalterado; a imagem roda limpa sob `--read-only`). Presente ⇒ o nó ABRE o Event Store
		// (WAL append-only + fsync + replay crash-safe) e o WORM (hash-chain tamper-evident) nos
		// caminhos dados — tipicamente um mount GRAVÁVEL fora do root-fs read-only (deploy/node:
		// -v aos-data:/var/lib/aos). É esta ligação que torna real o estado durável que o
		// Dockerfile documenta e que a durabilidade de kill+reinício-sem-duplicação exige.
		EventStorePath: eventStorePath,
		WORMPath:       strings.TrimSpace(os.Getenv("AOS_WORM_PATH")),
		// EXECUÇÃO DURÁVEL (AOS-180) por ambiente (AOS_DURABLE_EXECUTION — AOS-191): liga o
		// checkpointer, o capturer de não-determinismo e o step-ledger sobre o Event Store
		// durável já validado acima. Sem a variável fica false ⇒ os três permanecem nil
		// (comportamento actual, inalterado).
		DurableExecution: durableExecution,
		// Observabilidade OTLP (AOS-173): vazio ⇒ NoopTracer (default, zero overhead);
		// presente ⇒ o nó exporta traces (invoke_agent/chat[+custo]/execute_tool/freeze +
		// selos WORM) via OTLP/HTTP. Um endpoint malformado aborta o arranque (fail-closed).
		OTLPEndpoint: strings.TrimSpace(os.Getenv("AOS_OTLP_ENDPOINT")),
		// Operators vazio ⇒ default-deny do canal de controlo até serem configuradas
		// pubkeys de operador (o steer anónimo é recusado — a inércia do D4 não protege
		// pause/steer).
	}

	// DURABILIDADE DA IDENTIDADE (AOS-170) por ambiente, SÓ no modo de REFERÊNCIA. Um
	// AOS_ISSUER_KEY_PATH faz a autoridade co-localizada carregar/persistir a chave de assinatura
	// de um ficheiro de seed em vez de a gerar por CSPRNG a cada arranque — os tokens emitidos
	// antes do reinício continuam válidos. PROIBIDO no modo endurecido (nenhuma chave de assinatura
	// entra no processo): só se liga quando issuerPub == nil, senão o Bootstrap aborta fail-closed
	// com ErrConflictingIssuerKey.
	if issuerPub == nil {
		cfg.IssuerKeyPath = strings.TrimSpace(os.Getenv("AOS_ISSUER_KEY_PATH"))
	}

	return cfg, nil
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

// parseBoardRegions interpreta a config DEMO-GRADE do registo board→região (AOS-172, D7) na
// forma "board=regiao,board2=regiao2". FAIL-CLOSED e sem degradação silenciosa:
//
//   - VAZIO (ou só espaços) ⇒ (nil, nil): "não configurado", read-path legado DELIBERADO.
//   - segmentos VAZIOS entre vírgulas (ex.: vírgula final) são tolerados (ruído de formatação).
//   - qualquer entrada NÃO-VAZIA MALFORMADA (sem '=', ou board/região vazios) ⇒ (nil,
//     [ErrBadBoardRegions]): "configurado mas inválido", ABORTA. Um typo (ex.: "aos-demo"
//     sem '=') NÃO é descartado em silêncio para depois abrir TODAS as leituras sem authz
//     (D7) nem selo (D6) — a ambiguidade de soberania NEGA, coerente com o resto do boot.
//
// Coerente com o fail-closed de [govsov.NewRegistry] (que também descarta board/região
// vazios) mas mais estrito na FRONTEIRA de config: aqui um valor inválido aborta o arranque
// em vez de silenciosamente resultar num registo vazio ⇒ read-path aberto.
func parseBoardRegions(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil // não configurado ⇒ legado deliberado (não é um erro).
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (ex.: vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado board=regiao)", ErrBadBoardRegions, strings.TrimSpace(pair))
		}
		board, region := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if board == "" || region == "" {
			return nil, fmt.Errorf("%w: entrada %q com board ou regiao vazios", ErrBadBoardRegions, strings.TrimSpace(pair))
		}
		out[board] = region
	}
	if len(out) == 0 {
		// Só houve segmentos vazios (ex.: ",," ou ", ") — configurado mas sem entrada válida.
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadBoardRegions, s)
	}
	return out, nil
}

// parseDurableExecution interpreta AOS_DURABLE_EXECUTION (AOS-191) como um booleano
// EXPLÍCITO e previsível. FAIL-CLOSED de CONFIG, no padrão de [parseBoardRegions]:
//
//   - VAZIO (ausente, ou definido só com espaços) ⇒ (false, nil): execução durável
//     DESLIGADA — o default e o comportamento actual byte-a-byte. Não há aqui o
//     terceiro estado que AOS_BOARD_REGIONS precisa: ali o vazio DESLIGAVA uma
//     protecção ligada por omissão (kill-switch); aqui o vazio COINCIDE com o default.
//   - "1"/"true"/"t"/"yes"/"y"/"on" (qualquer caixa) ⇒ (true, nil).
//   - "0"/"false"/"f"/"no"/"n"/"off" (qualquer caixa) ⇒ (false, nil).
//   - QUALQUER OUTRO valor ⇒ (false, [ErrBadDurableExecution]): ABORTA. Tratar lixo como
//     false daria ao operador que escreveu "enabled"/"tru"/"sim" um nó que perde
//     checkpoints, capturas e step-ledger no reinício — silenciosamente, e a acreditar
//     que não perde. É deliberadamente mais estrito que strconv.ParseBool no lado dos
//     valores ACEITES (que documenta) e igualmente estrito no lado dos rejeitados.
func parseDurableExecution(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return false, nil // não configurado ⇒ desligado (o default) — não é um erro.
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%w: valor %q", ErrBadDurableExecution, strings.TrimSpace(s))
	}
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
