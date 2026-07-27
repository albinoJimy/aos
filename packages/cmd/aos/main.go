package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
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

// ErrBadOperators — AOS_OPERATORS presente mas com uma entrada MALFORMADA (sem '=', com
// emitterID vazio, com pubkey que não é ed25519 hex de 32 bytes, ou com emitterID DUPLICADO).
// Fail-closed de CONFIG do CANAL DE CONTROLO (AOS-193), no padrão exacto de
// [ErrBadBoardRegions]: distingue "não configurado" (vazio ⇒ default-deny deliberado, o canal
// continua inoperável e o bind não-loopback continua recusado) de "configurado mas inválido"
// (aborta). Um operador que TENCIONA registar a sua pubkey mas comete um typo NÃO obtém um nó
// que aceita o arranque e depois recusa TODOS os seus steer/pause com ErrUnknownEmitter — a
// ambiguidade NEGA o arranque. O DUPLICADO aborta em vez de "o último ganha": duas linhas com o
// mesmo emitterID e pubkeys diferentes são um conflito de autoridade, não uma preferência.
var ErrBadOperators = errors.New("aos: AOS_OPERATORS invalida (esperado \"emitterID=hexpubkey,emitterID2=hexpubkey2\" com pubkey ed25519 de 64 hex chars = 32 bytes — entrada malformada, emitterID duplicado ou a MESMA pubkey em dois emitterIDs abortam em vez de degradar silenciosamente para um canal de controlo inoperavel ou sem atribuicao)")

// ErrBadApproversFile — AOS_APPROVERS_FILE presente mas o ficheiro não é legível, não é um
// documento JSON válido no esquema esperado, ou tem uma entrada inválida (principal vazio,
// pubkey não-ed25519-hex, autoridade vazia ou fora do vocabulário, principal duplicado, DOIS
// principals com a MESMA pubkey, lista vazia). Fail-closed de CONFIG do 4-EYES (AOS-193), a
// contraparte de [ErrBadOperators] para o registo RICO dos aprovadores. Um ficheiro configurado
// mas inválido ABORTA: senão o nó arrancaria com [Node.FourEyes] == nil e POST
// /runs/{id}/approve continuaria a devolver 501 — exactamente a degradação silenciosa que o
// achado ORF-02 nomeia — ou, pior, com um gate COMPOSTO cujo dual-control uma única chave
// privada satisfaz (capacidade anunciada e não cumprida, na versão perigosa).
var ErrBadApproversFile = errors.New("aos: AOS_APPROVERS_FILE invalido (esperado JSON {\"approvers\":[{\"principal\":..., \"pubkey\":\"<64 hex>\", \"authority\":[\"approve:safe|gray|danger\"]}]} — ficheiro ilegivel, esquema invalido, principal duplicado, MESMA pubkey em dois principals, capability fora do vocabulario, autoridade vazia ou lista vazia abortam em vez de deixarem o four-eyes silenciosamente DESLIGADO ou ANULADO)")

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

	// KILL-SWITCH VISÍVEL da soberania de leitura (AOS-203, achado ORF-05). Em produção o
	// estado desligado nem chega aqui (ErrProductionNeedsSovereignRead abortou em
	// [nodeConfigFromEnv], e esse fail-closed NÃO é alterado); FORA de produção o
	// definido-vazio é aceite — mas deixa de ser SILENCIOSO. O aviso sai do lado do
	// entrypoint (a fronteira que LÊ a variável), a seguir ao banner do composition-root,
	// e nomeia o que fica desligado e como se religa. Ver [sovereignReadKillSwitchBanner].
	//
	// O predicado é o GATE REAL do read-path soberano, não uma aproximação: [NewAPIServer]
	// só compõe a read-governance quando o registo board→região E o WORM estão ambos
	// compostos (`node.SovereignReadRegions != nil && node.WORM != nil` — ver newReadGovernance
	// em api.go). Espelhá-lo aqui, em vez de olhar só para o registo, garante que o aviso
	// nunca pode ficar CALADO enquanto o nó serve o read-path legado: se o WORM alguma vez
	// se tornar opcional (hoje o Bootstrap cai sempre para audit.NewMemStore), o nó avisa em
	// vez de anunciar uma soberania que não está a aplicar — que é precisamente a promessa
	// falsa que AOS-203 vem eliminar.
	rawBoardRegions, boardRegionsDefined := os.LookupEnv("AOS_BOARD_REGIONS")
	for _, line := range sovereignReadKillSwitchBanner(node.SovereignReadRegions != nil, node.WORM != nil, rawBoardRegions, boardRegionsDefined) {
		fmt.Fprintf(w, "[aos] %s\n", line)
	}

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
	//
	// O segundo estado é um KILL-SWITCH de um controlo de conformidade. Em produção é RECUSADO
	// (ErrProductionNeedsSovereignRead, logo abaixo); fora de produção é aceite mas deixou de
	// ser silencioso — [run] escreve o aviso de [sovereignReadKillSwitchBanner] (AOS-203).
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
	// POSTURA DE PRODUÇÃO — DECIDIDA EM AOS-203: MANTÉM-SE OPT-IN, TAMBÉM EM
	// AOS_MODE=production. Ao contrário da identidade endurecida
	// (ErrProductionNeedsHardenedIdentity) e da soberania de leitura
	// (ErrProductionNeedsSovereignRead), AOS_MODE=production NÃO exige execução durável e NÃO
	// passará a exigir: um nó de produção arranca sem ela. Isto não é uma pendência — é a
	// decisão, e o eixo que AOS-191 abriu fica FECHADO aqui.
	//
	// CRITÉRIO que separa este caso dos outros dois: a PROMESSA FALSA. Ali o nó SERVIRIA
	// identidade/leituras com uma postura mais fraca do que a que um nó de produção
	// implicitamente anuncia; aqui não anuncia durabilidade nenhuma — o banner declara
	// "execucao duravel (AOS-180): DESLIGADA" em CADA arranque e nenhum endpoint promete
	// sobrevivência de checkpoints. Capacidade declaradamente ausente, não capacidade
	// anunciada e não cumprida. Argumentos subordinados: (i) exigi-la quebraria a
	// retro-compatibilidade que AOS-191 impõe e converteria um ticket de SUPERFÍCIE DE
	// CONFIGURAÇÃO numa mudança de postura de produção não anunciada aos operadores
	// existentes; (ii) o eixo REALMENTE perigoso — durabilidade LIGADA sobre substrato
	// volátil — já é fail-closed em QUALQUER modo, logo abaixo.
	//
	// A tabela comparativa das três posturas e a consequência para o operador (quem quiser
	// retoma de runs interrompidos TEM de a ligar explicitamente, mesmo em produção) estão em
	// deploy/node/README.md, secção "Postura de produção de AOS_DURABLE_EXECUTION — decisão
	// (AOS-203)". Reabrir isto exige emenda registada, não um PR silencioso.
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

	// CANAL DE CONTROLO AUTENTICADO (AOS-160) — PUBKEYS DOS OPERADORES por ambiente
	// (AOS_OPERATORS, AOS-193). É o caminho que faltava: sem ele [Config.Operators] era
	// INALCANÇÁVEL pelo binário entregue (Config vive em `package main`), o
	// Ed25519Authenticator ficava com ZERO emissores e devolvia ErrUnknownEmitter a TODO o
	// steer/pause — dois subcomandos da CLI e dois endpoints de controlo inatingíveis sem
	// forkar e recompilar (achado ORF-02).
	//
	// SÓ MATERIAL PÚBLICO. O valor é "emitterID=hexpubkey": 64 hex chars = 32 bytes de
	// pubkey ed25519, a MESMA codificação de AOS_ISSUER_PUBKEY. A chave PRIVADA do operador
	// vive na máquina do operador (é lá que `aos steer` assina, cli.go) e NUNCA entra aqui.
	//
	// LIMITE HONESTO desta validação: uma SEED ed25519 tem também 32 bytes, pelo que o nó
	// NÃO a consegue distinguir estruturalmente de uma pubkey. Um operador que colar a sua
	// seed obtém um registo que nunca verifica assinatura nenhuma (fail-closed, sem
	// elevação de privilégio) — mas terá exposto a sua chave privada ao ambiente do nó por
	// erro seu. Está documentado em deploy/node/README.md em vez de fingido em código.
	operators, err := parseOperators(os.Getenv("AOS_OPERATORS"))
	if err != nil {
		return Config{}, err
	}

	// FOUR-EYES / DUAL-CONTROL (AOS-162) — APROVADORES por FICHEIRO MONTADO
	// (AOS_APPROVERS_FILE, AOS-193). Ver [parseApproversFile] para a JUSTIFICAÇÃO de ser um
	// ficheiro e não uma env plana (o registo é rico: principal + pubkey + autoridade []).
	approvers, err := parseApproversFile(strings.TrimSpace(os.Getenv("AOS_APPROVERS_FILE")))
	if err != nil {
		return Config{}, err
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
		// fail-closed é FIXA; o provisioning real de regiões/boards fica DEFERIDO (AOS-205).
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
		// CANAL DE CONTROLO (AOS-160/AOS-193): pubkeys dos operadores lidas de AOS_OPERATORS
		// (já validadas fail-closed acima). Vazio ⇒ default-deny do canal de controlo (o steer
		// anónimo é recusado — a inércia do D4 não protege pause/steer) E, desde AOS-193, o
		// bind-guardrail RECUSA um bind não-loopback (um canal inoperável não é um canal
		// autenticado).
		Operators: operators,
		// FOUR-EYES (AOS-162/AOS-193): aprovadores lidos do ficheiro montado AOS_APPROVERS_FILE
		// (já validados fail-closed acima). Vazio ⇒ o gate NÃO é composto e POST
		// /runs/{id}/approve devolve 501 (endpoint declaradamente desligado, não uma falha).
		Approvers: approvers,
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

// sovereignReadKillSwitchBanner produz o AVISO PROEMINENTE do KILL-SWITCH da soberania de
// leitura (AOS-203, achado ORF-05 da auditoria v4). Devolve nil — nenhuma linha — quando o
// read-path soberano está LIGADO (o caso comum: sem AOS_BOARD_REGIONS o nó aplica o default
// de referência "board:aos-demo=eu").
//
// O DEFEITO QUE FECHA. `AOS_BOARD_REGIONS=` (DEFINIDA-VAZIA) é um kill-switch: desliga um
// controlo de CONFORMIDADE que está LIGADO por omissão — a authz por-chamador das leituras de
// governação (D7) e o selo WORM da leitura sensível (D6). Em `AOS_MODE=production` esse estado
// já é RECUSADO ([ErrProductionNeedsSovereignRead]) e este ticket NÃO altera esse fail-closed;
// fora de produção ele é legítimo (os harnesses de durabilidade e de plano de controlo usam-no
// deliberadamente), mas era SILENCIOSO: a única linha do banner era descritiva ("read-path
// LEGADO") e apontava para `Config.BoardRegions`, um campo de `package main` que o operador do
// binário nem consegue escrever. Um operador que herdasse um `-e AOS_BOARD_REGIONS=` de uma
// receita não tinha como saber o que tinha perdido.
//
// PORQUÊ AVISO E NÃO RECUSA (fora de produção). Recusar quebraria a retro-compatibilidade que
// a superfície de configuração impõe e cortaria um estado que o próprio projecto usa em
// harnesses. O critério é o mesmo de AOS-191: o que não se tolera é a PROMESSA FALSA — daí o
// aviso declarar, sem eufemismo, o que fica desligado.
//
// PORQUÊ AQUI E NÃO NO COMPOSITION-ROOT. O banner de [Bootstrap] declara o estado COMPOSTO
// (readRegions nil ⇒ "read-path LEGADO"); esta função declara a CAUSA DE AMBIENTE e o remédio,
// que só a fronteira que lê a variável conhece — um embedder que compõe [Config] à mão não
// tem AOS_BOARD_REGIONS nenhuma para religar.
//
// RESIDUAL CONHECIDO (não é omissão). A linha do composition-root ainda manda "definir
// Config.BoardRegions" — um campo de `package main` INALCANÇÁVEL por quem corre a imagem, isto
// é, metade do próprio defeito ORF-05 (sintoma com remédio inalcançável). Reescrevê-la exige
// tocar em bootstrap.go, fora da propriedade de ficheiros deste ticket; até lá, o aviso
// NEUTRALIZA-A explicitamente (a linha "IGNORE …" abaixo) em vez de a deixar a competir com o
// remédio verdadeiro — as duas linhas do banner ficam coerentes e o operador não segue a morta.
//
// Os parâmetros são o estado REALMENTE COMPOSTO, não a intenção da config: `regionsComposed`
// é Node.SovereignReadRegions != nil e `wormComposed` é Node.WORM != nil — a CONJUNÇÃO que
// api.go exige para compor a read-governance. O aviso nunca contradiz o que o nó está a fazer,
// e nunca fica calado por olhar para metade do gate.
func sovereignReadKillSwitchBanner(regionsComposed, wormComposed bool, rawBoardRegions string, defined bool) []string {
	if regionsComposed && wormComposed {
		return nil
	}
	// A causa é nomeada com precisão — cada estado produz uma mensagem diferente (o operador
	// tem de reconhecer o SEU caso na linha).
	var cause string
	switch {
	case regionsComposed && !wormComposed:
		// Inalcançável pelo caminho do binário (o Bootstrap cai sempre para audit.NewMemStore
		// quando não há WORM durável), mas é o estado em que a soberania seria anunciada e não
		// aplicada: o registo está configurado e o read-path é LEGADO na mesma. Se o WORM
		// alguma vez se tornar opcional, o operador vê a causa exacta em vez de silêncio.
		cause = "o registo board->regiao esta configurado MAS o WORM nao esta composto — o read-path soberano exige AMBOS (o selo D6 nao teria onde ser gravado), pelo que a soberania fica DESLIGADA apesar de AOS_BOARD_REGIONS ter valor"
	case defined && strings.TrimSpace(rawBoardRegions) == "":
		cause = "AOS_BOARD_REGIONS esta DEFINIDA-VAZIA (kill-switch explicito: a variavel existe no ambiente com valor vazio)"
	case !defined:
		// Inalcançável pelo caminho do binário (sem a variável aplica-se o default de
		// referência): só um Config composto in-process chega aqui. A linha diz a verdade
		// para esse caso em vez de mentir sobre uma variável que ninguem definiu.
		cause = "o registo board->regiao esta VAZIO e AOS_BOARD_REGIONS nao esta definida (Config.BoardRegions composta in-process sem entradas)"
	default:
		cause = "o registo board->regiao ficou VAZIO a partir de AOS_BOARD_REGIONS=" + strings.TrimSpace(rawBoardRegions)
	}
	return []string{
		"AVISO KILL-SWITCH (AOS-203): SOBERANIA DE LEITURA (AOS-172, D7) DESLIGADA — " + cause,
		"=> FICA DESLIGADO: (1) AUTHZ POR-CHAMADOR das leituras de governacao (D7) — o no serve TODAS as leituras sem exigir X-Aos-Reader/X-Aos-Board nem resolver a regiao autorizada do board; (2) SELO WORM da leitura sensivel (D6) — nao fica trilho tamper-evident de QUEM leu o que",
		"=> PARA RELIGAR: defina AOS_BOARD_REGIONS=\"board=regiao\" (ex.: AOS_BOARD_REGIONS=\"board:prod=eu\") ou REMOVA a variavel do ambiente para voltar ao default de referencia \"board:aos-demo=eu\"",
		"=> IGNORE a linha \"defina Config.BoardRegions\" do banner acima: Config.BoardRegions e um campo de codigo (package main) que quem corre o binario/imagem NAO consegue escrever — o unico remedio alcancavel e AOS_BOARD_REGIONS, na linha anterior",
		"=> AOS_MODE=production RECUSA arrancar neste estado (ErrProductionNeedsSovereignRead) — este aviso so existe porque o no NAO esta em modo de producao",
	}
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

// parseOperators interpreta AOS_OPERATORS (AOS-193) — o registo emitterID→PUBKEY dos
// operadores autorizados a emitir sinais de controlo (steer/pause/resume) — na forma
// "emitterID=hexpubkey,emitterID2=hexpubkey2".
//
// FORMATO: é DELIBERADAMENTE a gramática de [parseBoardRegions] (mesmo separador de entradas,
// mesmo separador chave/valor, mesma tolerância a segmentos vazios) com o valor codificado
// como em AOS_ISSUER_PUBKEY (32 bytes ed25519 em hex). [Config.Operators] é um mapa
// id→pubkey PLANO, pelo que a env plana o representa sem perda: não se inventa um formato
// novo para uma forma que o projecto já sabe exprimir.
//
// FAIL-CLOSED e sem degradação silenciosa:
//
//   - VAZIO (ou só espaços) ⇒ (nil, nil): "não configurado". O canal fica em DEFAULT-DENY
//     (nenhum sinal autentica) e — desde AOS-193 — o bind não-loopback é RECUSADO. É um
//     estado válido e declarado, não um erro.
//   - segmentos VAZIOS entre vírgulas (ex.: vírgula final) são tolerados (ruído de formatação).
//   - entrada sem '=', emitterID vazio, pubkey vazia/não-hex/de tamanho != 32 bytes, ou
//     emitterID DUPLICADO ⇒ [ErrBadOperators]: ABORTA. Nenhum destes casos pode degradar para
//     "regista os que der": um operador em falta significa que TODOS os seus sinais serão
//     recusados com ErrUnknownEmitter num nó que arrancou a anunciar um canal de controlo.
//   - dois emitterIDs DIFERENTES com a MESMA pubkey ⇒ [ErrBadOperators]: ABORTA. O steer não
//     tem invariante de distinção (ao contrário do 4-eyes), pelo que a partilha não eleva
//     privilégio — DESTRÓI A ATRIBUIÇÃO: um `aos steer --emitter ops:bob` assinado pela chave
//     de `ops:alice` seria aceite e SELADO NO WORM como sendo de `ops:bob`, e o nome do emissor
//     deixaria de ser evidência. É o mesmo conflito de autoridade do emitterID duplicado,
//     invertido (uma chave, dois nomes), e a mesma resposta: abortar, não escolher por nós.
//
// SÓ PUBKEYS. Nunca se aceita nem se lê material privado por esta porta; a chave privada do
// operador assina no processo DO OPERADOR (`aos steer`, cli.go).
func parseOperators(s string) (map[string]ed25519.PublicKey, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil // não configurado ⇒ default-deny deliberado (não é um erro).
	}
	out := make(map[string]ed25519.PublicKey)
	// seenKey indexa o material PÚBLICO já visto (hex da pubkey → emitterID que o trouxe) para
	// apanhar a colisão de chave entre emitterIDs distintos. É um índice de material público
	// mantido em memória durante o parse — nada dele é logado (ver o erro abaixo).
	seenKey := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		if strings.TrimSpace(pair) == "" {
			continue // segmento vazio (ex.: vírgula final) ⇒ ruído tolerável, não um typo.
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: entrada %q sem '=' (esperado emitterID=hexpubkey)", ErrBadOperators, strings.TrimSpace(pair))
		}
		id, rawKey := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if id == "" {
			return nil, fmt.Errorf("%w: entrada %q com emitterID vazio", ErrBadOperators, strings.TrimSpace(pair))
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%w: emitterID %q duplicado (conflito de autoridade — o ultimo NAO ganha)", ErrBadOperators, id)
		}
		pub, err := parseEd25519PubHex(rawKey)
		if err != nil {
			// A pubkey NUNCA é ecoada no erro (é público, mas ecoá-la enche os logs de
			// material de chave sem necessidade): identifica-se a entrada pelo emitterID.
			return nil, fmt.Errorf("%w: emitterID %q com pubkey invalida (esperado 64 hex chars = 32 bytes ed25519)", ErrBadOperators, id)
		}
		fp := hex.EncodeToString(pub)
		if other, dup := seenKey[fp]; dup {
			// A pubkey NÃO é ecoada: a entrada é identificada pelos DOIS emitterIDs em conflito.
			return nil, fmt.Errorf("%w: emitterIDs %q e %q partilham a MESMA pubkey (a atribuicao do steer deixaria de distinguir quem emitiu o sinal)", ErrBadOperators, other, id)
		}
		seenKey[fp] = id
		out[id] = pub
	}
	if len(out) == 0 {
		// Só houve segmentos vazios (ex.: ",," ou ", ") — configurado mas sem entrada válida.
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadOperators, s)
	}
	return out, nil
}

// approversDoc é o esquema do ficheiro de aprovadores (AOS_APPROVERS_FILE, AOS-193). Só
// material PÚBLICO: principal, pubkey ed25519 em hex e a autoridade autoritativa
// (capabilities "approve:<classe>" que o principal detém). A chave PRIVADA do aprovador vive
// no dispositivo do humano e nunca aqui (AOS-162).
type approversDoc struct {
	Approvers []approverDoc `json:"approvers"`
}

// approverDoc é UMA entrada do ficheiro de aprovadores.
type approverDoc struct {
	Principal string   `json:"principal"`
	PubKey    string   `json:"pubkey"`    // 64 hex chars = 32 bytes ed25519 (== AOS_ISSUER_PUBKEY)
	Authority []string `json:"authority"` // ex.: ["approve:danger", "approve:gray"]
}

// parseApproversFile lê o ficheiro MONTADO de aprovadores do FourEyesGate (AOS-162) apontado
// por AOS_APPROVERS_FILE (AOS-193).
//
// PORQUÊ UM FICHEIRO E NÃO UMA ENV (decisão, não omissão). [Config.Operators] é um mapa
// id→escalar e cabe sem perda numa env plana (ver [parseOperators]); [ApproverConfig] NÃO é
// escalar — traz `Authority []string` por entrada. Espremê-lo numa env exigiria um TERCEIRO
// nível de delimitador ("principal=hex;cap1;cap2,principal2=..."), uma gramática que o projecto
// não usa em lado nenhum, ilegível a partir de 2 capabilities e sem forma de ser revista em
// code-review. Um ficheiro JSON montado é a via que o ADR-017 ponto 2 já prevê ("Config por
// env/ficheiro montado"), é versionável e é exactamente o artefacto que uma roster de
// aprovadores de dual-control deve ser. A COERÊNCIA com [parseOperators] mantém-se onde importa:
// a MESMA codificação hex do material público (a de AOS_ISSUER_PUBKEY), a MESMA disciplina
// fail-closed (malformado/duplicado/vazio ABORTA) e o MESMO invariante (só pubkeys entram).
// Cada colaborador tem UM caminho de configuração — não há precedência env-vs-ficheiro a
// divergir em silêncio.
//
// FAIL-CLOSED:
//
//   - path VAZIO ⇒ (nil, nil): four-eyes NÃO configurado; o gate não é composto e
//     POST /runs/{id}/approve devolve 501 (desligado por declaração).
//   - ficheiro ilegível, JSON malformado ou com campos DESCONHECIDOS ⇒ [ErrBadApproversFile]
//     (o `DisallowUnknownFields` apanha um esquema em drift — ex.: "pub_key" — em vez de o
//     ignorar e compor um gate incompleto; é a mesma disciplina do decodeJSON da API).
//   - lista VAZIA, principal vazio, principal DUPLICADO, pubkey inválida, ou autoridade vazia
//     ⇒ ABORTA. A autoridade vazia é rejeitada porque um aprovador sem NENHUMA capability
//     `approve:<classe>` nunca satisfaz [hitl.RequiredAuthority]: seria uma entrada que parece
//     configurada e nunca aprova nada — a degradação silenciosa que este ticket vem fechar.
//   - capability FORA do vocabulário fechado ("approve:safe"|"approve:gray"|"approve:danger",
//     ver [approveCapabilities]) ⇒ ABORTA. hitl.RequiredAuthority só produz estes três valores e
//     a comparação em integration.hasAuthority é de string EXACTA (sem wildcards): um typo
//     ("approve:dangerous", "approve:*") daria exactamente o estado anterior — uma entrada que
//     parece configurada, contada no banner, e que nunca aprova nada.
//   - duas entradas com a MESMA PUBKEY (mesmo com principals diferentes) ⇒ ABORTA. Esta é a
//     guarda de SEGURANÇA do ficheiro, não de higiene: a distinção estrutural do dual-control
//     (integration.FourEyesGate.authorizeDual) compara approver/session/credential, TRÊS
//     strings ESCOLHIDAS PELO CLIENTE na perna; a única âncora criptográfica de "duas pessoas"
//     é a pubkey PINADA. Com a mesma pubkey em duas linhas, UMA só chave privada assina as duas
//     pernas e o 4-eyes é anulado EM SILÊNCIO, com o banner a declarar "2 aprovador(es)
//     pinados". Antes de AOS-193 esse roster exigia forkar e recompilar; agora é um ficheiro
//     montado, e um copy-paste é suficiente para o produzir.
func parseApproversFile(path string) ([]ApproverConfig, error) {
	if path == "" {
		return nil, nil // não configurado ⇒ four-eyes desligado por declaração (não é um erro).
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler %q: %v", ErrBadApproversFile, path, err)
	}
	var doc approversDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrBadApproversFile, path, err)
	}
	if len(doc.Approvers) == 0 {
		return nil, fmt.Errorf("%w: %q sem aprovadores (um ficheiro configurado e vazio deixaria o four-eyes DESLIGADO em silencio)", ErrBadApproversFile, path)
	}
	out := make([]ApproverConfig, 0, len(doc.Approvers))
	seen := make(map[string]struct{}, len(doc.Approvers))
	// seenKey: hex do material PÚBLICO → principal que o trouxe (ver a guarda de colisão abaixo).
	seenKey := make(map[string]string, len(doc.Approvers))
	for i, a := range doc.Approvers {
		principal := strings.TrimSpace(a.Principal)
		if principal == "" {
			return nil, fmt.Errorf("%w: %q entrada #%d com principal vazio", ErrBadApproversFile, path, i)
		}
		if _, dup := seen[principal]; dup {
			return nil, fmt.Errorf("%w: %q principal %q duplicado (conflito de autoridade — o ultimo NAO ganha)", ErrBadApproversFile, path, principal)
		}
		seen[principal] = struct{}{}
		pub, perr := parseEd25519PubHex(strings.TrimSpace(a.PubKey))
		if perr != nil {
			return nil, fmt.Errorf("%w: %q principal %q com pubkey invalida (esperado 64 hex chars = 32 bytes ed25519)", ErrBadApproversFile, path, principal)
		}
		if other, dup := seenKey[hex.EncodeToString(pub)]; dup {
			// A pubkey NÃO é ecoada: identificam-se os DOIS principals que a partilham.
			return nil, fmt.Errorf("%w: %q principals %q e %q partilham a MESMA pubkey — UMA chave privada assinaria as DUAS pernas e o dual-control (AOS-162) ficaria anulado", ErrBadApproversFile, path, other, principal)
		}
		seenKey[hex.EncodeToString(pub)] = principal
		authority := splitTrimmed(a.Authority)
		if len(authority) == 0 {
			return nil, fmt.Errorf("%w: %q principal %q sem autoridade (um aprovador sem capability approve:<classe> nunca autoriza nada)", ErrBadApproversFile, path, principal)
		}
		for _, capability := range authority {
			if !isApproveCapability(capability) {
				return nil, fmt.Errorf("%w: %q principal %q com capability %q desconhecida (vocabulario fechado: %s — a comparacao e de string EXACTA, sem wildcards)",
					ErrBadApproversFile, path, principal, capability, strings.Join(approveCapabilities(), ", "))
			}
		}
		out = append(out, ApproverConfig{Principal: principal, PubKey: pub, Authority: authority})
	}
	return out, nil
}

// splitTrimmed normaliza uma lista de strings: remove espaços e descarta as vazias. Devolve
// nil quando não sobra nenhuma (⇒ o chamador trata "configurado mas vazio" fail-closed).
func splitTrimmed(in []string) []string {
	var out []string
	for _, v := range in {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
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
