// AOS-166 — a API HTTP (net/http stdlib) que torna o nó `aos` OPERÁVEL de fora,
// resolvendo os dois achados ALTO do painel: (nº4) o canal de CONTROLO sem autenticação e
// (nº5) o INGRESSO sem admission. Grada o [NodeService] long-running de AOS-164a — que só
// existia como superfície in-process — para uma superfície de rede, SEM sair da stdlib
// (net/http, encoding/json, crypto) e SEM afrouxar nenhuma invariante de segurança.
//
// PRINCÍPIOS (ADR-016: canal de controlo TRUSTED separado do de dados; BFF non-signing):
//
//   - SEPARAÇÃO CONTROL/DATA. POST /runs é o plano de DADOS (submete um goal). Os POSTs de
//     CONTROLO (/steer, /pause, /approve) são o plano TRUSTED: cada um exige um emitter
//     ed25519 e é AUTENTICADO na fronteira de segurança REAL do nó (o Ed25519Authenticator
//     de AOS-160 via node.Steer, ou o FourEyesGate de AOS-162). NUNCA um POST de controlo
//     anónimo é aceite.
//
//   - NON-SIGNING (ADR-016 §1). A API NUNCA gera nem detém a chave privada do operador. A
//     assinatura VEM no corpo (no emitter) — produzida FORA deste processo, no dispositivo
//     do humano/serviço. A API só a TRANSPORTA ao autenticador, que a verifica contra a
//     PUBKEY pinada. Uma assinatura inválida/replayada/velha ⇒ REJEITADA fail-closed, sem
//     efeito no SteerChannel.
//
//   - BIND-GUARDRAIL. [APIServer.Serve] RECUSA (erro, não um mero log) fazer bind a um
//     endereço NÃO-loopback enquanto a autenticação do canal de controlo não estiver ligada
//     (node.SteerAuth == nil ou o modo de identidade não for real). Expor o canal de
//     controlo à rede sem autenticação seria precisamente o achado nº4 — o guardrail
//     torna-o IMPOSSÍVEL por construção, não por convenção.
//
//   - ADMISSION (achado nº5). POST /runs passa por um token-bucket (rate-limit) E por um
//     tecto de runs em curso; exceder qualquer um ⇒ 429. Todos os corpos passam por
//     [http.MaxBytesReader] (⇒ 413 no excesso) para o ingresso não ser vector de exaustão.
//
//   - FAIL-CLOSED e NÃO-ENUMERÁVEL. Um pedido malformado/não-autenticado/RunID inexistente é
//     recusado SEM efeito e sem vazar a existência de runs alheios: GET de um RunID
//     inexistente e de um não-observável devolvem o MESMO 404 uniforme. Simetricamente,
//     POST /runs de um RunID JÁ conhecido (em curso/terminado nesta réplica ou com lease
//     noutra) devolve o MESMO 201 "accepted" IDEMPOTENTE que uma submissão fresca — o
//     status nunca distingue "existe" de "novo", pelo que um chamador anónimo (o plano de
//     dados é não-autenticado por ADR-016) não o pode usar como oráculo de existência.
//
// FRONTEIRA de escopo: a trajectória em streaming (SSE) é AOS-167 — aqui GET /runs/{id} é
// a fotografia do estado/desfecho. A API usa o mux de método+padrão da stdlib (Go 1.22+),
// pelo que não há router externo.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
)

// Defaults da API (todos endurecíveis por [APIOption]).
const (
	// DefaultMaxBodyBytes é o tecto por omissão do corpo de um pedido (1 MiB). Um goal ou
	// um sinal de controlo legítimo cabe folgadamente; o limite corta um corpo gigante
	// ANTES de ele esgotar memória (achado nº5).
	DefaultMaxBodyBytes int64 = 1 << 20
	// DefaultRateBurst é a capacidade por omissão do token-bucket de admission de POST /runs.
	DefaultRateBurst = 128
	// DefaultRatePerSec é o reabastecimento por omissão (tokens/segundo) do token-bucket.
	DefaultRatePerSec = 64
	// DefaultMaxInFlight é o tecto por omissão de runs EM CURSO admissíveis por esta réplica.
	// Exceder ⇒ 429 (o ingresso não sobrecarrega o loop de serviço). <= 0 desliga o tecto.
	DefaultMaxInFlight = 512
	// DefaultReadHeaderTimeout limita quanto tempo se espera pelos cabeçalhos (anti
	// slowloris). Os restantes timeouts do http.Server derivam dele.
	DefaultReadHeaderTimeout = 5 * time.Second
	// DefaultReadTimeout / DefaultWriteTimeout / DefaultIdleTimeout completam a defesa do
	// http.Server contra conexões lentas/pendentes.
	DefaultReadTimeout  = 15 * time.Second
	DefaultWriteTimeout = 15 * time.Second
	DefaultIdleTimeout  = 60 * time.Second
	// DefaultTrajectoryWriteTimeout é o write-deadline POR-ESCRITA do stream SSE de
	// trajectória (AOS-167). NÃO é um tecto da ligação inteira (essa é longa por natureza):
	// limita CADA escrita individual no socket. Um cliente que parou de ler faz a escrita
	// bloquear; o deadline dispara e a ligação cai (drop-slow-consumer por progresso). Um
	// cliente que drena completa cada escrita dentro do deadline e sobrevive indefinidamente.
	DefaultTrajectoryWriteTimeout = 15 * time.Second
	// DefaultMaxTrajectoryConns é o tecto por omissão de streams SSE de trajectória
	// concorrentes por-nó (AOS-167). Cada stream mantém 1 subscrição + goroutines vivas até
	// o cliente desligar; o tecto barra a exaustão de recursos dentro da fronteira de
	// confiança (coerente com o hardening de ingresso de AOS-166). Exceder ⇒ 429. <= 0
	// desliga o tecto.
	DefaultMaxTrajectoryConns = 256
)

// Erros da API (fail-closed).
var (
	// ErrRefuseNonLoopbackBind — o bind-guardrail RECUSOU um bind a um endereço não-loopback
	// porque a autenticação do canal de controlo não está ligada. Expor o canal de controlo
	// à rede sem authn é o achado nº4; o guardrail fecha-o na construção do Listen.
	//
	// A mensagem nomeia PRECISAMENTE o que o predicado verifica — steer/pause — e não "o canal
	// de controlo" inteiro: um nó configurado SÓ com AOS_APPROVERS_FILE tem o /approve
	// plenamente operável e é, ainda assim, recusado. Dizer "canal de controlo não operável"
	// afirmaria mais do que [APIServer.controlAuthenticated] verifica — e foi exactamente essa
	// discrepância entre descrição e predicado (STR-04) que AOS-193 veio fechar, pelo que não
	// se reintroduz no texto do erro.
	ErrRefuseNonLoopbackBind = errors.New("aos/api: bind a endereco NAO-loopback RECUSADO — steer/pause INOPERAVEIS (SteerAuth ausente, identidade nao-real, ou ZERO operadores registados); use loopback, ou ligue a identidade real E registe pelo menos um operador em AOS_OPERATORS")
	// ErrRefuseCleartextBind — o bind-guardrail RECUSOU um bind a um endereço não-loopback em
	// TEXTO-CLARO (AOS-209). É a QUARTA conjunção da mesma disciplina de [guardBind]: expor
	// o ingresso (API/SSE/DSAR) à rede sem TLS deixa o transporte legível por qualquer
	// intermediário — a assinatura ed25519 do canal de controlo permanece íntegra, mas o
	// conteúdo transportado (trajectória, desfechos, corpos de sinais) é observável, o que
	// corrói o valor prático da autenticação (achado §5.2-b de tecnica/17).
	//
	// A recusa fecha-se na CONSTRUÇÃO do Listen, como as outras conjunções. É contornável de
	// DUAS formas explícitas, nunca por omissão silenciosa: (a) terminar TLS NO nó
	// (AOS_TLS_CERT_PATH + AOS_TLS_KEY_PATH), ou (b) DECLARAR terminação TLS a montante
	// (AOS_TLS_EXTERNAL_TERMINATION), que assume a responsabilidade e emite um aviso ruidoso
	// no banner. Loopback continua sempre permitido (só o próprio host alcança a porta).
	ErrRefuseCleartextBind = errors.New("aos/api: bind a endereco NAO-loopback em TEXTO-CLARO RECUSADO (AOS-209) — o ingresso serviria API/SSE/DSAR sem TLS e qualquer intermediario leria o transporte; termine TLS no no (AOS_TLS_CERT_PATH+AOS_TLS_KEY_PATH), ou DECLARE terminacao a montante (AOS_TLS_EXTERNAL_TERMINATION=1), ou use loopback")
	// ErrBadTLSKeyPair — AOS_TLS_CERT_PATH/AOS_TLS_KEY_PATH presentes mas o par certificado+chave
	// não carrega (ficheiro ilegível, PEM malformado, chave que não corresponde ao certificado).
	// Fail-closed: um nó não sobe a servir TLS com um par inválido.
	ErrBadTLSKeyPair = errors.New("aos/api: par TLS invalido (AOS_TLS_CERT_PATH/AOS_TLS_KEY_PATH ilegivel, PEM malformado, ou chave que nao corresponde ao certificado)")
	// ErrIncompleteTLSConfig — só um de AOS_TLS_CERT_PATH/AOS_TLS_KEY_PATH foi definido. Fail-closed:
	// a terminação TLS no nó exige AMBOS (certificado E chave), e a ambiguidade aborta em vez de
	// degradar em silêncio para texto-claro.
	ErrIncompleteTLSConfig = errors.New("aos/api: config TLS incompleta — AOS_TLS_CERT_PATH e AOS_TLS_KEY_PATH sao AMBOS obrigatorios para terminar TLS no no (definir so um aborta em vez de servir em claro)")
	// ErrBadControlMTLSCA — AOS_CONTROL_MTLS_CA_PATH presente mas o bundle de CA de cliente
	// não carrega (ficheiro ilegível, ou sem nenhum certificado PEM válido). Fail-closed de
	// CONFIG (DEF-012, EIXO 1): um nó não sobe a anunciar mTLS do plano de controlo com uma
	// âncora de confiança que nunca verificaria um certificado de cliente.
	ErrBadControlMTLSCA = errors.New("aos/api: AOS_CONTROL_MTLS_CA_PATH invalido (ficheiro ilegivel ou sem certificado PEM de CA de cliente valido)")
	// ErrControlMTLSNeedsNodeTLS — AOS_CONTROL_MTLS_CA_PATH está definido mas o nó NÃO termina
	// TLS (AOS_TLS_CERT_PATH+AOS_TLS_KEY_PATH ausentes). A autenticação MÚTUA acontece no
	// handshake TLS, que só o nó que TERMINA TLS realiza — não se pode exigir certificado de
	// cliente a montante (terminação externa) nem em texto-claro. Fail-closed: a ambiguidade
	// aborta em vez de anunciar um mTLS que o listener nunca imporia.
	ErrControlMTLSNeedsNodeTLS = errors.New("aos/api: AOS_CONTROL_MTLS_CA_PATH exige terminacao TLS NO no (AOS_TLS_CERT_PATH+AOS_TLS_KEY_PATH) — o mTLS do plano de controlo verifica o certificado de cliente no handshake, que so o no que termina TLS realiza; nao e compativel com terminacao a montante nem com texto-claro")
	// ErrControlClientCertRequired — o mTLS do plano de controlo está LIGADO e o pedido a uma
	// rota de controlo (/steer, /pause, /approve) chegou SEM um certificado de cliente
	// verificado contra a CA montada. É a PRIMEIRA barreira (transporte) da defesa-em-profundidade
	// de DEF-012; a segunda (assinatura ed25519 no corpo, AOS-160) é imposta a seguir por
	// node.Steer/FourEyes. Recusar aqui NUNCA substitui a assinatura — é ADITIVO a ela.
	ErrControlClientCertRequired = errors.New("aos/api: rota de controlo RECUSADA — mTLS do plano de controlo LIGADO exige certificado de cliente verificado (a assinatura ed25519 do corpo continua a ser exigida a seguir; o mTLS e uma barreira ADICIONAL, nao um bypass)")
	// ErrNilService / ErrNilNode — construção sem os colaboradores obrigatórios.
	ErrNilService = errors.New("aos/api: NewAPIHandler exige um NodeService (nil)")
	ErrNilNode    = errors.New("aos/api: NewAPIHandler exige um Node (nil)")
)

// mTLS DO PLANO DE CONTROLO — ENTREGUE OPT-IN (DEF-012, EIXO 1). A terminação TLS de AOS-209
// cifra e autentica o SERVIDOR perante o cliente; ESTE eixo acrescenta a autenticação MÚTUA por
// certificado de CLIENTE às rotas de controlo (/steer, /pause, /approve), composta por
// [WithControlMTLS] a partir de uma CA de cliente montada por FICHEIRO (AOS_CONTROL_MTLS_CA_PATH).
//
// DESENHO — ESCOPADO ao plano de controlo, NÃO listener-wide. O listener negoceia
// tls.VerifyClientCertIfGiven (pede e VERIFICA o certificado de cliente contra a CA montada SE
// apresentado, mas não o exige a TODAS as rotas), e SÓ os handlers de controlo RECUSAM
// ([admitControlMTLS] ⇒ [ErrControlClientCertRequired]) quando não há PeerCertificate verificado.
// Assim /healthz, /readyz, GET/POST /runs e /trajectory — que NÃO pertencem ao plano de controlo
// (sondas de orquestrador não assinam; o plano de dados é não-autenticado por ADR-016) — não
// passam a exigir certificado de cliente. RequireAndVerifyClientCert no listener imporia o
// certificado a essas rotas, fora do âmbito do ticket.
//
// ADITIVO, NUNCA BYPASS. O mTLS é uma SEGUNDA barreira (transporte); a PRIMEIRA continua a ser a
// assinatura ed25519 no corpo (non-signing, AOS-160), imposta a seguir por node.Steer/FourEyes,
// independente do transporte. Um certificado de cliente válido com assinatura AUSENTE/MÁ continua
// RECUSADO. Requer terminação TLS NO nó ([ErrControlMTLSNeedsNodeTLS]).
//
// A autenticação forte da perna OTLP perante o colector (EIXO 2) é o par deste eixo — ver o
// marcador em otlpexporter.go. O que fica deferido em DEF-012 é apenas a PROVISÃO de infra
// (PKI/emissão de certificados de cliente aos operadores, bearer do colector), não código do nó —
// ver DEF-012 em docs/governance/REGISTO-Deferimentos.md (nota N-DEF-012).

// apiConfig é a configuração resolvida da API.
type apiConfig struct {
	maxBodyBytes     int64
	rateBurst        float64
	ratePerSec       float64
	ctrlRateBurst    float64
	ctrlRatePerSec   float64
	maxInFlight      int
	trajWriteTimeout time.Duration // write-deadline por-escrita do SSE de trajectória
	trajMaxConns     int           // tecto de streams SSE concorrentes por-nó
	serverWriteTO    time.Duration // WriteTimeout do http.Server (0 ⇒ DefaultWriteTimeout)
	now              func() time.Time
	logw             io.Writer
	// --- Terminação TLS do ingresso (AOS-209) --------------------------------
	// tlsCertPath/tlsKeyPath apontam para o certificado e a CHAVE PRIVADA montados por
	// ficheiro (padrão AOS_ISSUER_KEY_PATH: material privado NUNCA por variável de ambiente).
	// Ambos vazios ⇒ ingresso em texto-claro (só admitido em loopback ou com externalTLS).
	// Ambos definidos ⇒ [NewAPIServer] carrega o par e serve TLS endurecido. Só um ⇒
	// [ErrIncompleteTLSConfig].
	tlsCertPath string
	tlsKeyPath  string
	// externalTLS DECLARA que a terminação TLS é feita a MONTANTE (ingress/malha de serviço):
	// o nó serve em claro POR DECISÃO explícita de quem o configurou. Satisfaz a quarta
	// conjunção do bind-guardrail (o transporte É cifrado, apenas não no nó) e faz o banner
	// emitir um aviso proeminente. É mutuamente exclusivo com a terminação no nó (um par TLS
	// definido tem precedência: se o nó termina TLS, não há terminação "externa" a declarar).
	externalTLS bool
	// controlMTLSCAPath aponta para o bundle PEM da CA de CLIENTE montado por ficheiro
	// (DEF-012, EIXO 1). Vazio ⇒ mTLS do plano de controlo DESLIGADO (comportamento actual:
	// só a assinatura ed25519 autentica). Definido ⇒ o listener verifica o certificado de
	// cliente contra esta CA (VerifyClientCertIfGiven) e as rotas /steer,/pause,/approve
	// RECUSAM sem um certificado verificado — ADITIVO à assinatura ed25519. Exige terminação
	// TLS no nó ([ErrControlMTLSNeedsNodeTLS]). Material PÚBLICO (a CA), por ficheiro montado.
	controlMTLSCAPath string
	// readGov é a costura de SOBERANIA/CONFORMIDADE de leitura (AOS-172, D7+D6). Quando
	// composta (explicitamente por [WithReadSovereignty] ou auto-derivada do nó em
	// [NewAPIHandler]), os handlers de leitura passam a exigir authz por-chamador (board→região
	// fail-closed) e a selar cada leitura sensível no WORM. nil ⇒ read-path legado (sem authz
	// por-chamador, sem selo) — a topologia soberana é condicional ao provisioning (deferido).
	readGov *readGovernance
}

// APIOption configura a API HTTP.
type APIOption func(*apiConfig)

// WithMaxBodyBytes define o tecto do corpo de um pedido (default [DefaultMaxBodyBytes]).
// Valores <= 0 são ignorados.
func WithMaxBodyBytes(n int64) APIOption {
	return func(c *apiConfig) {
		if n > 0 {
			c.maxBodyBytes = n
		}
	}
}

// WithRateLimit define o token-bucket de admission de POST /runs: burst (capacidade) e
// perSec (reabastecimento). Um burst <= 0 mantém o default; perSec < 0 é ignorado (0 é
// válido: bucket sem reabastecimento — útil em testes determinísticos).
func WithRateLimit(perSec, burst float64) APIOption {
	return func(c *apiConfig) {
		if burst > 0 {
			c.rateBurst = burst
		}
		if perSec >= 0 {
			c.ratePerSec = perSec
		}
	}
}

// WithControlRateLimit define o token-bucket de admission do PLANO DE CONTROLO (/steer,
// /pause, /approve): burst (capacidade) e perSec (reabastecimento). Semântica idêntica a
// [WithRateLimit], mas sobre um bucket DEDICADO — o plano de controlo trusted tem o seu
// próprio tecto de taxa, para que uma inundação de sinais (cada um a forçar um decode +
// ed25519.Verify) não esgote CPU nem esfomeie o plano de dados, e vice-versa. Um burst <= 0
// mantém o default [DefaultRateBurst]; perSec < 0 é ignorado (0 é válido: bucket sem
// reabastecimento, útil em testes determinísticos).
func WithControlRateLimit(perSec, burst float64) APIOption {
	return func(c *apiConfig) {
		if burst > 0 {
			c.ctrlRateBurst = burst
		}
		if perSec >= 0 {
			c.ctrlRatePerSec = perSec
		}
	}
}

// WithMaxInFlight define o tecto de runs em curso admissíveis (default [DefaultMaxInFlight]).
// <= 0 desliga o tecto (só o rate-limit governa a admission).
func WithMaxInFlight(n int) APIOption {
	return func(c *apiConfig) { c.maxInFlight = n }
}

// WithTrajectoryWriteTimeout define o write-deadline POR-ESCRITA do stream SSE de trajectória
// (AOS-167; default [DefaultTrajectoryWriteTimeout]). NÃO limita a duração da ligação — limita
// cada escrita individual no socket, que é o gatilho do drop-slow-consumer. Um valor pequeno
// torna o corte de um consumidor preso mais rápido (útil em testes determinísticos de
// backpressure); <= 0 mantém o default.
func WithTrajectoryWriteTimeout(d time.Duration) APIOption {
	return func(c *apiConfig) {
		if d > 0 {
			c.trajWriteTimeout = d
		}
	}
}

// WithMaxTrajectoryConns define o tecto de streams SSE de trajectória concorrentes por-nó
// (AOS-167; default [DefaultMaxTrajectoryConns]). Exceder ⇒ 429. <= 0 desliga o tecto (útil
// em testes que abrem muitas ligações). É a admission anti-exaustão do read-path tempo-real,
// coerente com o hardening de ingresso de AOS-166.
func WithMaxTrajectoryConns(n int) APIOption {
	return func(c *apiConfig) { c.trajMaxConns = n }
}

// WithServerWriteTimeout define o WriteTimeout do http.Server endurecido de [NewAPIServer]
// (default [DefaultWriteTimeout]). É um deadline POR-LIGAÇÃO que a rota de trajectória SSE
// ANULA para si própria (transporte fail-safe); expor este knob permite provar em teste que a
// ligação SSE sobrevive para além dele. Um valor <= 0 mantém o default.
func WithServerWriteTimeout(d time.Duration) APIOption {
	return func(c *apiConfig) {
		if d > 0 {
			c.serverWriteTO = d
		}
	}
}

// WithAPIClock injecta o relógio do token-bucket (determinismo em teste de admission, sem
// sleeps). Ignora nil.
func WithAPIClock(now func() time.Time) APIOption {
	return func(c *apiConfig) {
		if now != nil {
			c.now = now
		}
	}
}

// WithAPILog injecta o destino dos logs da API (arranque/guardrail). nil ⇒ sem logs.
func WithAPILog(w io.Writer) APIOption {
	return func(c *apiConfig) { c.logw = w }
}

// WithReadSovereignty compõe EXPLICITAMENTE a costura de SOBERANIA/CONFORMIDADE do read-path
// (AOS-172, D7+D6): o registo board→região (AOS-094) que autoriza o leitor por-chamador e o
// WORM onde cada leitura sensível é selada. Tem PRECEDÊNCIA sobre o auto-wiring de
// [NewAPIHandler] a partir do nó — serve testes/deployments que injectam um Registry próprio
// ou um WORM específico (ex.: um WORM que falha o Append, para provar a negação fail-closed de
// D6). Ignora (mantém legado) se regions ou worm forem nil.
func WithReadSovereignty(regions *govsov.Registry, worm audit.Store) APIOption {
	return func(c *apiConfig) {
		if regions != nil && worm != nil {
			c.readGov = newReadGovernance(regions, nil, worm, c.now)
		}
	}
}

// WithSovereignAuthority compõe o read-path soberano sobre a FONTE DE AUTORIDADE board→região
// (AOS-205, [SovereignRegionAuthority] com rotação+auditoria) e, opcionalmente, a CREDENCIAL
// FORTE do leitor (cred). Tem PRECEDÊNCIA sobre o auto-wiring de [NewAPIHandler]. Quando cred é
// composto, o board/reader são derivados das CLAIMS VERIFICADAS (o header X-Aos-Board deixa de
// autorizar); cred nil mantém a via legada por headers (só fora de produção). Ignora (mantém
// legado) se authority ou worm forem nil.
func WithSovereignAuthority(authority *SovereignRegionAuthority, cred readCredentialVerifier, worm audit.Store) APIOption {
	return func(c *apiConfig) {
		if authority != nil && worm != nil {
			c.readGov = newReadGovernance(authority, cred, worm, c.now)
		}
	}
}

// WithTLSFiles compõe a TERMINAÇÃO TLS do ingresso NO nó (AOS-209): o certificado e a CHAVE
// PRIVADA são lidos dos ficheiros MONTADOS certPath/keyPath (o padrão de AOS_ISSUER_KEY_PATH —
// material privado por ficheiro, NUNCA por variável de ambiente). [NewAPIServer] carrega o par
// (fail-closed: um par inválido ⇒ [ErrBadTLSKeyPair]) e monta uma [tls.Config] endurecida
// (MinVersion TLS 1.2, cipher suites AEAD/ECDHE). Só um dos dois caminhos ⇒
// [ErrIncompleteTLSConfig]. Ambos vazios ⇒ sem terminação no nó.
func WithTLSFiles(certPath, keyPath string) APIOption {
	return func(c *apiConfig) {
		c.tlsCertPath = strings.TrimSpace(certPath)
		c.tlsKeyPath = strings.TrimSpace(keyPath)
	}
}

// WithControlMTLS compõe o mTLS do PLANO DE CONTROLO (DEF-012, EIXO 1): a CA de CLIENTE é lida
// do ficheiro MONTADO caPath (material PÚBLICO por ficheiro, no padrão de [WithTLSFiles]).
// [NewAPIServer] carrega o bundle (fail-closed: bundle inválido ⇒ [ErrBadControlMTLSCA]), exige
// que o nó TERMINE TLS ([ErrControlMTLSNeedsNodeTLS] caso contrário) e liga
// tls.VerifyClientCertIfGiven + ClientCAs no listener. As rotas /steer,/pause,/approve passam a
// RECUSAR ([ErrControlClientCertRequired]) um pedido sem certificado de cliente verificado — uma
// SEGUNDA barreira ADITIVA à assinatura ed25519 do corpo (AOS-160), nunca um substituto dela.
// caPath vazio ⇒ mTLS de controlo desligado (comportamento actual).
func WithControlMTLS(caPath string) APIOption {
	return func(c *apiConfig) { c.controlMTLSCAPath = strings.TrimSpace(caPath) }
}

// WithExternalTLSTermination DECLARA que a terminação TLS é feita A MONTANTE (ingress/malha):
// o nó serve em texto-claro por DECISÃO explícita de quem o configurou (AOS-209). Satisfaz a
// quarta conjunção do bind-guardrail — o transporte é cifrado, apenas não no nó — e faz o
// banner emitir um aviso proeminente. Ignorada quando há terminação TLS no nó ([WithTLSFiles]):
// se o nó já termina TLS, não há terminação externa a declarar.
func WithExternalTLSTermination(declared bool) APIOption {
	return func(c *apiConfig) { c.externalTLS = declared }
}

// apiHandler é o http.Handler do nó: um mux stdlib sobre o [NodeService] (plano de dados) e
// o [Node] (plano de controlo real). Seguro para uso concorrente na medida em que os
// colaboradores o são.
type apiHandler struct {
	svc        *NodeService
	node       *Node
	cfg        apiConfig
	bucket     *tokenBucket // admission do plano de DADOS (POST /runs)
	ctrlBucket *tokenBucket // admission do plano de CONTROLO (/steer, /pause, /approve)
	trajConns  atomic.Int64 // nº de streams SSE de trajectória concorrentes (admission)
	// controlMTLS indica se o mTLS do plano de controlo está LIGADO (DEF-012, EIXO 1). Quando
	// true, os handlers de controlo exigem um certificado de cliente verificado — ADITIVO à
	// assinatura ed25519. O ClientCAs/ClientAuth vive no listener ([NewAPIServer]); este flag é
	// a contraparte no handler que faz a RECUSA por-rota.
	controlMTLS bool
	// readGov é a costura de soberania/conformidade de leitura (AOS-172, D7+D6). nil ⇒ legado.
	readGov *readGovernance
	// expireInFlight serializa as passagens do [audit.ExpirationJob] conduzidas por
	// POST /dsar/expire (AOS-213). O Run do job é pensado para uma execução de cada vez
	// (o check-then-Add da idempotency key não é atómico ao nível do registo), pelo que
	// duas passagens concorrentes poderiam selar DOIS eventos retention.expired para o
	// mesmo facto. O guard CAS admite UMA passagem activa; uma segunda invocação
	// concorrente recebe 409 (no-op) em vez de poluir a cadeia WORM.
	expireInFlight atomic.Bool
}

// NewAPIHandler compõe o http.Handler do nó sobre o loop de serviço (AOS-164a) e o nó real
// (AOS-163). É directamente exercitável com httptest. Fail-closed: svc e node são
// obrigatórios.
func NewAPIHandler(svc *NodeService, node *Node, opts ...APIOption) (http.Handler, error) {
	if svc == nil {
		return nil, ErrNilService
	}
	if node == nil {
		return nil, ErrNilNode
	}
	cfg := apiConfig{
		maxBodyBytes:     DefaultMaxBodyBytes,
		rateBurst:        DefaultRateBurst,
		ratePerSec:       DefaultRatePerSec,
		ctrlRateBurst:    DefaultRateBurst,
		ctrlRatePerSec:   DefaultRatePerSec,
		maxInFlight:      DefaultMaxInFlight,
		trajWriteTimeout: DefaultTrajectoryWriteTimeout,
		trajMaxConns:     DefaultMaxTrajectoryConns,
		now:              time.Now,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	// SOBERANIA/CONFORMIDADE de leitura (AOS-172, D7+D6). Precedência: uma composição
	// EXPLÍCITA ([WithReadSovereignty]) vence; senão AUTO-DERIVA do nó quando este traz um
	// registo board→região composto (o caminho de PRODUÇÃO fica fail-closed por CONSTRUÇÃO, não
	// por lembrança de passar uma opção). Um nó sem soberania configurada (SovereignReadRegions
	// nil) mantém o read-path legado — a regra é fixa, a topologia é condicional (deferido).
	readGov := cfg.readGov
	if readGov == nil && node.WORM != nil {
		switch {
		case node.SovereignAuthority != nil:
			// AOS-205: a FONTE DE AUTORIDADE (rotação+auditoria) e — quando composta — a
			// CREDENCIAL FORTE do leitor. cred nil (fora de produção) ⇒ via legada por headers
			// sobre a autoridade; cred composta ⇒ board/reader das claims verificadas.
			readGov = newReadGovernance(node.SovereignAuthority, node.SovereignReadCredential, node.WORM, cfg.now)
		case node.SovereignReadRegions != nil:
			// Caminho de compatibilidade: um registo board→região cru sem autoridade composta.
			readGov = newReadGovernance(node.SovereignReadRegions, nil, node.WORM, cfg.now)
		}
	}
	h := &apiHandler{
		svc:         svc,
		node:        node,
		cfg:         cfg,
		bucket:      newTokenBucket(cfg.rateBurst, cfg.ratePerSec, cfg.now),
		ctrlBucket:  newTokenBucket(cfg.ctrlRateBurst, cfg.ctrlRatePerSec, cfg.now),
		controlMTLS: cfg.controlMTLSCAPath != "",
		readGov:     readGov,
	}

	mux := http.NewServeMux()
	// SONDAS de orquestrador (AOS-171, E5): liveness + readiness. SEM autenticação (probes
	// de k8s/orquestrador não assinam) e SEM admission/rate-limit (probes são frequentes;
	// passá-las pelo token-bucket causaria falso-unready). NÃO consomem os buckets de
	// admitData/admitControl nem o tecto de trajConns. O padrão método+rota da stdlib
	// (Go 1.22+) já devolve 405 a métodos != GET.
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	mux.HandleFunc("GET /readyz", h.handleReadyz)
	// Observabilidade MÉTRICA (revisão de prontidão #5): via de métricas em texto Prometheus. O
	// nó só exportava traces — os SLOs (disponibilidade, saúde de dependências, USE) são métricos e
	// eram indetectáveis. Não-autenticada como healthz/readyz (scrapers não assinam; sem PII/segredos;
	// restringir por rede). Zero-dep: texto emitido à mão, sem SDK de métricas.
	mux.HandleFunc("GET /metrics", h.handleMetrics)
	// Plano de DADOS.
	mux.HandleFunc("POST /runs", h.handleSubmit)
	mux.HandleFunc("GET /runs/{id}", h.handleGet)
	// Plano de DADOS — read-path TEMPO-REAL (AOS-167): SSE dos eventos da trajectória.
	mux.HandleFunc("GET /runs/{id}/trajectory", h.handleTrajectory)
	// Plano de DADOS — RECONSTRUÇÃO SOBERANA de conteúdo selado (AOS-214): decifra o conteúdo
	// cifrado por-titular (AOS-093) de um run para um leitor autorizado por soberania (D7+D6).
	// Desligado (501) sem o gate soberano composto. Ver sovereign_replay.go.
	mux.HandleFunc("GET /runs/{id}/reconstruct", h.handleReconstruct)
	// Plano de CONTROLO TRUSTED (cada um autenticado na fronteira real do nó).
	mux.HandleFunc("POST /runs/{id}/steer", h.handleSteer)
	mux.HandleFunc("POST /runs/{id}/pause", h.handlePause)
	mux.HandleFunc("POST /runs/{id}/approve", h.handleApprove)
	mux.HandleFunc("POST /runs/{id}/resume", h.handleResume)
	// Plano de GOVERNANÇA — DSAR / crypto-shredding (AOS-172, Art. 17). Autenticado pelo gate
	// soberano de leitura + admission do plano de controlo; desligado se o fluxo não estiver
	// composto (fail-closed). Ver handleDSAR.
	mux.HandleFunc("POST /dsar/erase", h.handleDSAR)
	// Plano de GOVERNANÇA — ADMINISTRAÇÃO de legal hold e expiração (AOS-213, CON-02/DEF-903).
	// Autenticadas pela MESMA credencial forte do /dsar/erase (readGov) + admission do plano de
	// controlo; desligadas (501) se o legal hold / job de expiração ou o gate soberano não
	// estiverem compostos (fail-closed). Ver legalhold.go.
	mux.HandleFunc("POST /dsar/hold", h.handleHold)
	mux.HandleFunc("POST /dsar/release", h.handleRelease)
	mux.HandleFunc("POST /dsar/expire", h.handleExpire)
	return mux, nil
}

// ---------------------------------------------------------------------------
// Plano de DADOS — POST /runs, GET /runs/{id}
// ---------------------------------------------------------------------------

// submitRequest é a representação de wire de um goal submetido. Deliberadamente mínima: o
// tipo agentruntime.Goal tem campos ricos (tools/skills/model) que não fazem parte da
// superfície de submissão de AOS-166; aqui aceita-se o essencial e o RM real medeia o
// resto.
type submitRequest struct {
	RunID        string   `json:"run_id"`
	Objective    string   `json:"objective"`
	PrincipalNHI string   `json:"principal_nhi"`
	Credential   string   `json:"credential,omitempty"`
	Scope        []string `json:"scope,omitempty"`
	System       string   `json:"system,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
}

// submitResponse devolve o RunID hospedado (201).
type submitResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// handleSubmit é o INGRESSO do plano de dados com ADMISSION (achado nº5): (1) rate-limit
// por token-bucket + tecto de runs em curso; (2) limite de corpo; (3) submete ao serviço.
func (h *apiHandler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	// (1) ADMISSION antes de ler o corpo — rejeita cedo, não desperdiça trabalho num pedido
	// que não vai ser admitido.
	if !h.bucket.allow() {
		writeError(w, http.StatusTooManyRequests, "rate limit excedido")
		return
	}
	if h.cfg.maxInFlight > 0 && h.svc.InProgressCount() >= h.cfg.maxInFlight {
		writeError(w, http.StatusTooManyRequests, "tecto de runs em curso atingido")
		return
	}

	// (2) LIMITE DE CORPO + descodificação.
	var req submitRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "run_id em falta")
		return
	}

	// SOBERANIA — RESIDÊNCIA DO RUN na CRIAÇÃO (AOS-182, DEF-202). Em modo SOBERANO (gate de
	// leitura composto) a região de residência do run é ESTABELECIDA aqui a partir da resolução
	// board→região do SUBMISSOR — a MESMA autoridade (readGov.authorize) que resolve os leitores,
	// não uma auto-declaração sem verificação — e SELADA de forma durável e tamper-evidente
	// POR-RunID ANTES de o run ser hospedado, para que uma leitura futura possa exigir
	// leitor.região == run.região e nenhum run soberano fique legível antes de a sua residência
	// estar durável. FAIL-CLOSED: sem região resolvível (credencial/board ausente ou desconhecido)
	// o submit é RECUSADO; se o WORM não selar, é RECUSADO. Em modo LEGADO (readGov nil) nada muda
	// — sem residência, sem check cross-region, como o resto do read-path D6/D7 sem topologia
	// soberana.
	if h.readGov != nil {
		submitter, ok := h.readGov.authorize(r)
		if !ok {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		// AOS-217 (achado A1+A7) — TITULAR FAIL-CLOSED, DERIVADO DA CREDENCIAL VERIFICADA. Em modo
		// SOBERANO o TITULAR do run (o `Subject` sob cuja chave por-titular AOS-093 cifra o conteúdo
		// não-determinístico — texto do modelo, outputs de tools — ANTES do WAL do Event Store) é
		// DERIVADO do `submitter.principal` que [readGovernance.authorize] resolveu da credencial
		// VERIFICADA (OIDC/mTLS ou header demonstrativo fora de produção), NÃO do campo de corpo
		// auto-declarado
		// `req.PrincipalNHI` — que aqui é IGNORADO. Fecha A7 (titular desacoplado da credencial): o
		// submissor deixa de poder escolher um titular arbitrário (ou vazio) para o run que hospeda.
		// FAIL-CLOSED: [authorize] já NEGA (403) um submissor sem principal resolvível (principal
		// vazio ⇒ (_, false)) — a linha 564 acima converte esse false em 403 e retorna, pelo que
		// quando o fluxo alcança a guarda abaixo `submitter.principal` é já garantidamente != "".
		// Nenhum run soberano é hospedado sem um `Subject` sob o qual cifrar; a guarda explícita
		// abaixo é DEFESA EM PROFUNDIDADE — dado o contrato de [authorize] é hoje inalcançável, mas
		// torna o invariante LOCAL e auditável no ponto de uso: sem ele, um principal vazio degradaria
		// em silêncio para a cifra bypassed de `nondeterminism_capture.go:246`, persistindo o conteúdo
		// em CLARO e não-shreddable. Em modo
		// LEGADO (`readGov` nil) nada muda: o titular continua a vir de `req.PrincipalNHI` e runs fora
		// de produção/soberania não são forçados (retro-compat).
		if submitter.principal == "" {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		if err := h.readGov.sealResidency(r.Context(), submitter, req.RunID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "indisponivel")
			return
		}
		req.PrincipalNHI = submitter.principal
	}

	goal := agentruntime.Goal{
		RunID:      req.RunID,
		Objective:  req.Objective,
		Credential: req.Credential,
		Scope:      req.Scope,
		System:     req.System,
		MaxTurns:   req.MaxTurns,
	}
	goal.Principal.NHIID = req.PrincipalNHI

	// (3) SUBMETE ao loop de serviço. O ctx do pedido governa SÓ a aquisição do lease; o run
	// sobrevive ao retorno (é cancelado só por Shutdown).
	err := h.svc.Submit(r.Context(), goal)
	if err != nil {
		if isIdempotentResubmit(err) {
			// NÃO-ENUMERÁVEL + IDEMPOTENTE. Um run_id que ESTA réplica já hospeda/hospedou, ou
			// cujo lease é detido por OUTRA réplica, é tratado como RE-SUBMISSÃO idempotente:
			// devolve o MESMO 201 "accepted" que uma submissão fresca. Antes destes casos
			// devolvia-se 409 — um oráculo de existência: como o plano de dados é
			// não-autenticado (ADR-016), um chamador anónimo distinguia "existe" (409) de
			// "novo" (201) só pelo status, contradizendo a garantia não-enumerável que o GET
			// preserva. Com a resposta uniforme, o status de POST /runs deixa de revelar a
			// existência de um run alheio. O run NÃO é re-hospedado nem o desfecho
			// sobrescrito — a posse por lease e o registo por RunID continuam a mediá-lo a
			// jusante; o desfecho real fica atrás do GET (também não-enumerável).
			writeJSON(w, http.StatusCreated, submitResponse{RunID: req.RunID, Status: "accepted"})
			return
		}
		writeError(w, submitErrorStatus(err), "submissao recusada")
		return
	}
	writeJSON(w, http.StatusCreated, submitResponse{RunID: req.RunID, Status: "accepted"})
}

// isIdempotentResubmit indica se o erro de [NodeService.Submit] corresponde a uma
// RE-SUBMISSÃO de um run_id já conhecido (em curso/terminado nesta réplica, ou com lease
// detido noutra) — casos que a API trata como aceitação IDEMPOTENTE e NÃO-ENUMERÁVEL (mesmo
// run_id ⇒ mesma resposta 201 "accepted"), em vez de um 409 que vazaria a existência do run
// a um chamador anónimo do plano de dados.
func isIdempotentResubmit(err error) bool {
	return errors.Is(err, ErrRunAlreadyInProgress) ||
		errors.Is(err, ErrRunAlreadyCompleted) ||
		errors.Is(err, ErrRunLeaseHeldElsewhere) ||
		// SUSPENSO à espera de humano (AOS-021): é a MESMA classe — um run_id que esta
		// réplica conhece e cuja submissão não produz trabalho novo. Sem este caso caía no
		// default e devolvia 500, que lê como avaria do nó quando é uma recusa legítima.
		// O estado real (waiting_on_human + o que falta decidir) obtém-se em
		// GET /runs/{id}, que corre sob a credencial forte do read-path soberano — é lá que
		// a verdade se conta, não numa resposta de submissão potencialmente anónima.
		errors.Is(err, ErrRunSuspended)
}

// submitErrorStatus mapeia os erros GENUÍNOS de [NodeService.Submit] a códigos HTTP (os
// casos de re-submissão idempotente são interceptados ANTES — ver [isIdempotentResubmit]).
// A mensagem no corpo é uniforme; só o status distingue as classes, e nenhuma delas revela
// a existência de um run alheio.
func submitErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrEmptyRunID):
		return http.StatusBadRequest
	case errors.Is(err, ErrServiceShuttingDown):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// runStateResponse é a fotografia do estado/desfecho de um run (GET). A trajectória em
// streaming é AOS-167.
type runStateResponse struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"` // "in_progress" | "waiting_on_human" | "completed"
	Terminated bool   `json:"terminated,omitempty"`
	Paused     bool   `json:"paused,omitempty"`
	Panicked   bool   `json:"panicked,omitempty"`
	Error      string `json:"error,omitempty"`
	FinalText  string `json:"final_text,omitempty"`
	Turns      int    `json:"turns,omitempty"`
	// PendingApprovals são as tool calls ESCALADAS que aguardam aval humano (AOS-021).
	// É o que o operador consulta por POLLING para saber o que decidir; a preview é o
	// valor que cada perna de aprovação assina em POST /runs/{id}/approve. Ausente
	// quando não há nada por decidir (ou o four-eyes não está composto).
	PendingApprovals []pendingApprovalWire `json:"pending_approvals,omitempty"`
}

// pendingApprovalWire é a face de wire de uma aprovação pendente. Descreve O QUE vai
// executar (tool/capability/recurso) para o humano decidir com contexto.
//
// NÃO transporta o Input da tool — o payload pode conter dados sensíveis e não deve
// atravessar a superfície de administração. A amarra criptográfica é a `preview`, que já
// cobre o input POR HASH: assinar a preview é assinar exactamente aquela acção com
// exactamente aqueles argumentos (WYSIWYS).
type pendingApprovalWire struct {
	StepID         string `json:"step_id"`
	Turn           int    `json:"turn"`
	ToolID         string `json:"tool_id"`
	Capability     string `json:"capability"`
	ResourceType   string `json:"resource_type,omitempty"`
	ResourceValue  string `json:"resource_value,omitempty"`
	ResourceRegion string `json:"resource_region,omitempty"`
	Preview        string `json:"preview"` // base64 — o digest a assinar em /approve
}

// pendingApprovalsFor resolve as aprovações pendentes de um run para a resposta. Devolve
// nil quando o four-eyes não está composto ou nada está por decidir. Uma falha de leitura
// NÃO quebra a resposta de estado: o pendente é informação de administração, e degradar
// para "sem pendentes" é preferível a negar a consulta do estado do run (o operador
// percebe pela ausência; o run continua suspenso e nada executa).
func (h *apiHandler) pendingApprovalsFor(ctx context.Context, runID string) []pendingApprovalWire {
	if h.node.PendingApprovals == nil {
		return nil
	}
	recs, err := h.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil || len(recs) == 0 {
		return nil
	}
	out := make([]pendingApprovalWire, 0, len(recs))
	for _, r := range recs {
		out = append(out, pendingApprovalWire{
			StepID:         r.StepID,
			Turn:           r.Turn,
			ToolID:         r.ToolID,
			Capability:     r.Capability,
			ResourceType:   r.ResourceType,
			ResourceValue:  r.ResourceValue,
			ResourceRegion: r.ResourceRegion,
			Preview:        base64.StdEncoding.EncodeToString(r.Preview),
		})
	}
	return out
}

// handleGet devolve o estado/desfecho de um run. NÃO-ENUMERÁVEL: um RunID que esta réplica
// não hospeda nem reteve devolve o MESMO 404 uniforme que um não-observável — nada distingue
// "nunca existiu" de "existe mas não é seu".
func (h *apiHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// (D7) AUTHZ SOBERANA POR-CHAMADOR. Board→região fail-closed; leitor não autorizado ⇒ o
	// MESMO 404 uniforme de um run inexistente (não-enumerável, sem PII). Gate não composto ⇒
	// legado. Feita ANTES da verificação de existência: um leitor não autorizado nunca
	// distingue "existe" de "nao existe" — ambos 404.
	reader, residency, ok := h.admitSovereignRead(w, r, runID)
	if !ok {
		return
	}
	// SUSPENSO à espera de aval humano (AOS-021)? Verificado ANTES de "terminado": um run
	// escalado NÃO terminou — reportá-lo como "completed" seria uma mentira operacional
	// (o operador julgaria o trabalho feito quando falta a decisão dele).
	if oc, susp := h.svc.Suspended(r.Context(), runID); susp {
		if !h.sealSensitiveRead(w, r, reader, residency, runID, capReadOutcome) {
			return
		}
		writeJSON(w, http.StatusOK, runStateResponse{
			RunID:            runID,
			Status:           "waiting_on_human",
			Turns:            oc.Result.Turns,
			PendingApprovals: h.pendingApprovalsFor(r.Context(), runID),
		})
		return
	}
	// Terminado (desfecho retido)?
	if oc, done := h.svc.Outcome(runID); done {
		// (D6) SELO WORM de leitura sensível como PRÉ-CONDIÇÃO: se o WORM não selar, NEGA
		// fail-closed (não se serve o desfecho sensível sem o registo de auditabilidade).
		if !h.sealSensitiveRead(w, r, reader, residency, runID, capReadOutcome) {
			return
		}
		resp := runStateResponse{
			RunID:      runID,
			Status:     "completed",
			Terminated: oc.Result.Terminated,
			Paused:     oc.Result.Paused,
			Panicked:   oc.Panicked,
			FinalText:  oc.Result.FinalText,
			Turns:      oc.Result.Turns,
		}
		if oc.Err != nil {
			resp.Error = oc.Err.Error()
		}
		// Um run TERMINADO pode ter deixado pendentes por decidir (ex.: expirou o TTL e
		// voltou a correr sem a acção). Expô-los mantém o histórico legível ao operador.
		resp.PendingApprovals = h.pendingApprovalsFor(r.Context(), runID)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Em curso?
	for _, id := range h.svc.InProgress() {
		if id == runID {
			if !h.sealSensitiveRead(w, r, reader, residency, runID, capReadOutcome) {
				return
			}
			// É AQUI que o operador vê o que tem de aprovar: um run suspenso em
			// waiting_on_human continua "in_progress" e traz a lista de pendentes com a
			// preview a assinar em POST /runs/{id}/approve (AOS-021, polling).
			writeJSON(w, http.StatusOK, runStateResponse{
				RunID:            runID,
				Status:           "in_progress",
				PendingApprovals: h.pendingApprovalsFor(r.Context(), runID),
			})
			return
		}
	}
	// Desconhecido ⇒ 404 UNIFORME (não vaza existência de runs alheios).
	writeError(w, http.StatusNotFound, "not found")
}

// ---------------------------------------------------------------------------
// SONDAS de orquestrador (AOS-171, E5) — GET /healthz (liveness), GET /readyz (readiness)
// ---------------------------------------------------------------------------

// handleHealthz é a sonda de LIVENESS: o processo está vivo e a servir ⇒ 200 SEMPRE
// (enquanto o handler responde). NÃO consulta dependências — um /healthz que virasse 503
// por o Event Store estar em degradação causaria restart-loops em orquestração (o
// orquestrador MATA e reinicia um contentor cuja liveness falha, quando o correcto perante
// uma dependência indisponível é PARAR de encaminhar tráfego — isso é readiness). O corpo
// é mínimo e ESTÁVEL, sem detalhes internos.
func (h *apiHandler) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz é a sonda de READINESS (padrão k8s de drain): 200 SÓ SE (a) o Event Store
// está operacional (NÃO ErrClosed — [eventstore.Store.Healthy]) E (b) o serviço NÃO está
// em drain ([NodeService.Draining]); 503 caso contrário. Durante o shutdown gracioso o
// serviço arma o drain ANTES de esperar os runs escoarem, pelo que /readyz vira 503 de
// imediato (o orquestrador para de encaminhar tráfego novo) enquanto /healthz permanece
// 200 até o processo sair — é esta transição ready→unready que torna o drain seguro. O
// corpo é UNIFORME e mínimo: não revela contagem de runs, RunIDs, modo de identidade nem
// o detalhe do erro interno (coerente com a filosofia não-enumerável de handleGet); só o
// status HTTP distingue pronto de não-pronto.
func (h *apiHandler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.svc.Draining() || h.node.EventStore == nil || !h.node.EventStore.Healthy() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready"})
		return
	}
	// DEPENDÊNCIA CRÍTICA: a custódia da KEK (Vault). Se está configurada (custódia durável) e
	// SELADA/inalcançável, o crypto-shred/DSAR e a cifra de conteúdo sob a KEK estão quebrados —
	// 503 para o orquestrador PARAR de encaminhar, em vez de servir com a via GDPR partida em
	// silêncio (revisão de prontidão #2). O vault in-memory de referência NÃO implementa a sonda
	// (a KEK está sempre disponível em memória) ⇒ readyz não sonda nada, comportamento inalterado.
	if p, ok := h.node.DSARVault.(readinessProber); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := p.ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// readinessProber é uma dependência crítica que sabe dizer se está operacional. O DSARVault do
// Vault implementa-o (sonda seal-status); o vault in-memory de referência NÃO — nesse caso o
// /readyz não sonda a custódia (a KEK em memória está sempre disponível).
type readinessProber interface {
	ready(context.Context) error
}

// handleMetrics emite métricas operacionais em formato de exposição Prometheus (text/plain
// version 0.0.4). É a VIA DE MÉTRICAS que faltava (revisão de prontidão #5): sem ela os SLOs
// métricos — disponibilidade, saúde de dependências, saturação de recursos (USE) — eram
// indetectáveis a partir do nó em execução. Conjunto inicial produzível SEM instrumentar o
// kernel: gauges de saúde/dependência (o gap exato do achado) + runtime Go. As latências de
// request (mediation_overhead_p95) exigem histogramas instrumentados no kernel — follow-up. O
// corpo NÃO revela RunIDs/contagens sensíveis (coerente com a filosofia não-enumerável).
func (h *apiHandler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	g := func(name, help string, typ string, val float64, labels string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s%s %g\n", name, help, name, typ, name, labels, val)
	}
	b01 := func(ok bool) float64 {
		if ok {
			return 1
		}
		return 0
	}

	g("aos_up", "O processo esta a servir a API (sempre 1 quando responde).", "gauge", 1, "")
	g("aos_build_info", "Metadados do no (valor sempre 1; ler pelas labels).", "gauge", 1,
		fmt.Sprintf("{identity_mode=%q}", h.node.IdentityMode))

	draining := h.svc.Draining()
	g("aos_draining", "O servico esta em drain (1) para shutdown gracioso.", "gauge", b01(draining), "")

	esHealthy := h.node.EventStore != nil && h.node.EventStore.Healthy()
	g("aos_eventstore_healthy", "Event Store operacional (1) ou fechado/ausente (0).", "gauge", b01(esHealthy), "")

	// Custódia da KEK (Vault): dependência crítica cujo estado selado/inalcançável partia a via
	// GDPR em silêncio (revisão #2). Só exposta quando configurada (custódia durável).
	vaultReady := true
	if p, ok := h.node.DSARVault.(readinessProber); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		vaultReady = p.ready(ctx) == nil
		cancel()
		g("aos_dsar_vault_ready", "Custodia da KEK (Vault) operacional (1) ou selada/inalcancavel (0).", "gauge", b01(vaultReady), "")
	}

	// aos_ready espelha o veredito do /readyz (drain + Event Store + custódia da KEK) — o SLI de
	// disponibilidade a partir do qual o alerta de SLO dispara.
	g("aos_ready", "O no reporta-se pronto no /readyz (1) ou nao (0).", "gauge", b01(!draining && esHealthy && vaultReady), "")

	// Runtime Go (USE): saturação de recursos do processo.
	g("aos_goroutines", "Goroutines em execucao.", "gauge", float64(runtime.NumGoroutine()), "")
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	g("aos_memory_alloc_bytes", "Heap alocado e ainda em uso (bytes).", "gauge", float64(ms.Alloc), "")
	g("aos_memory_sys_bytes", "Memoria obtida do SO (bytes).", "gauge", float64(ms.Sys), "")
	g("aos_gc_cycles_total", "Ciclos de GC completos desde o arranque.", "counter", float64(ms.NumGC), "")

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

// ---------------------------------------------------------------------------
// Plano de CONTROLO TRUSTED — /steer, /pause, /approve (AUTENTICADO, non-signing)
// ---------------------------------------------------------------------------

// emitterWire é a representação de wire do control.Emitter: a IDENTIDADE + a ASSINATURA que
// o operador produziu FORA deste processo. A API só a transporta (non-signing).
type emitterWire struct {
	ID        string    `json:"id"`
	Signature string    `json:"signature"` // base64(assinatura ed25519)
	Nonce     string    `json:"nonce"`     // base64(nonce de uso-único)
	IssuedAt  time.Time `json:"issued_at"` // RFC3339 — frescura
}

// decode converte o emitter de wire em control.Emitter. Um campo base64 malformado ⇒ erro
// (o pedido é 400 SEM tocar no canal).
func (e emitterWire) decode() (control.Emitter, error) {
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return control.Emitter{}, fmt.Errorf("signature base64: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return control.Emitter{}, fmt.Errorf("nonce base64: %w", err)
	}
	return control.Emitter{ID: e.ID, Signature: sig, Nonce: nonce, IssuedAt: e.IssuedAt}, nil
}

// steerRequest transporta o emitter assinado + a correcção (o payload que a assinatura
// cobre). A correcção é base64 para ser binário-segura e bater byte-a-byte com o que foi
// assinado.
type steerRequest struct {
	Emitter emitterWire `json:"emitter"`
	Payload string      `json:"payload"` // base64(correccao)
}

// pauseRequest transporta só o emitter assinado (o pause não carrega payload).
type pauseRequest struct {
	Emitter emitterWire `json:"emitter"`
}

// handleSteer injecta uma correcção AUTENTICADA. A API é non-signing: passa o emitter
// (assinatura ed25519 produzida no dispositivo do operador) a node.Steer.Steer, que
// AUTENTICA na fronteira real (AOS-160). Assinatura inválida/replay/stale ⇒ 403 SEM efeito.
func (h *apiHandler) handleSteer(w http.ResponseWriter, r *http.Request) {
	if !h.admitControl(w) {
		return
	}
	if !h.admitControlMTLS(w, r) {
		return
	}
	runID := r.PathValue("id")
	var req steerRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	emitter, err := req.Emitter.decode()
	if err != nil {
		writeError(w, http.StatusBadRequest, "emitter invalido")
		return
	}
	correction, err := base64.StdEncoding.DecodeString(req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "payload invalido")
		return
	}
	if err := h.node.Steer.Steer(r.Context(), runID, correction, emitter); err != nil {
		writeError(w, controlErrorStatus(err), "sinal recusado")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "steered"})
}

// handlePause emite um pause gracioso AUTENTICADO (mesma fronteira do steer).
func (h *apiHandler) handlePause(w http.ResponseWriter, r *http.Request) {
	if !h.admitControl(w) {
		return
	}
	if !h.admitControlMTLS(w, r) {
		return
	}
	runID := r.PathValue("id")
	var req pauseRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	emitter, err := req.Emitter.decode()
	if err != nil {
		writeError(w, http.StatusBadRequest, "emitter invalido")
		return
	}
	if err := h.node.Steer.Pause(r.Context(), runID, emitter); err != nil {
		writeError(w, controlErrorStatus(err), "sinal recusado")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "paused"})
}

// controlErrorStatus mapeia os erros do canal de controlo a códigos HTTP. Uma falha de
// AUTENTICAÇÃO (assinatura inválida / emissor desconhecido / replay / stale) ⇒ 403
// (control.ErrUnauthenticated); um pedido estruturalmente inválido ⇒ 400. Mensagem uniforme
// no corpo (não-enumerável).
func controlErrorStatus(err error) int {
	switch {
	case errors.Is(err, control.ErrUnauthenticated):
		return http.StatusForbidden
	case errors.Is(err, control.ErrEmptyRunID),
		errors.Is(err, control.ErrEmptyEmitterID),
		errors.Is(err, control.ErrEmptyCorrection):
		return http.StatusBadRequest
	default:
		// Divergência de log / falha de store ⇒ 500 (sem detalhe no corpo).
		return http.StatusInternalServerError
	}
}

// approveRequest transporta a decisão irreversível a autorizar e as pernas assinadas do
// 4-eyes (AOS-162). Cada perna traz a assinatura ed25519 do aprovador (non-signing: a API
// nunca assina; só transporta).
type approveRequest struct {
	Request fourEyesRequestWire `json:"request"`
	Legs    []approvalLegWire   `json:"legs"`
}

// fourEyesRequestWire é a representação de wire de integration.FourEyesRequest.
type fourEyesRequestWire struct {
	RequestID           string `json:"request_id"`
	Preview             string `json:"preview"` // base64(digest do efeito exibido — WYSIWYS)
	RiskClass           uint8  `json:"risk_class"`
	DualControlRequired bool   `json:"dual_control_required"`
}

// approvalLegWire é a representação de wire de integration.ApprovalLeg.
type approvalLegWire struct {
	Approver   string `json:"approver"`
	Session    string `json:"session"`
	Credential string `json:"credential"`
	Challenge  string `json:"challenge"` // base64
	// DeviceAttestation/DeviceClientData transportam o par WebAuthn (attestationObject +
	// clientDataJSON) em base64. São VERIFICADOS quando o gate foi composto com a porta
	// integration.DeviceAttestationVerifier (AOS-177); no binário zero-dep do nó a porta
	// não é ligada (a impl vive no componente de autoridade externo — Carta emenda 1.3),
	// pelo que aqui são transportados sem verificação. O wire é o mesmo nos dois modos.
	DeviceAttestation string `json:"device_attestation,omitempty"`
	DeviceClientData  string `json:"device_client_data,omitempty"`
	Signature         string `json:"signature"` // base64(assinatura ed25519 da perna)
}

// handleApprove autoriza uma acção irreversível via o FourEyesGate (AOS-162), SE o nó o
// compôs; senão o endpoint está DESLIGADO (501). Non-signing: as assinaturas das pernas
// vêm no corpo; o gate verifica-as contra as pubkeys pinadas. Negação ⇒ 403 SEM efeito.
func (h *apiHandler) handleApprove(w http.ResponseWriter, r *http.Request) {
	if !h.admitControl(w) {
		return
	}
	if !h.admitControlMTLS(w, r) {
		return
	}
	if h.node.FourEyes == nil {
		writeError(w, http.StatusNotImplemented, "four-eyes desligado (sem aprovadores compostos)")
		return
	}
	var req approveRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	preview, err := base64.StdEncoding.DecodeString(req.Request.Preview)
	if err != nil {
		writeError(w, http.StatusBadRequest, "preview invalido")
		return
	}
	feReq := integration.FourEyesRequest{
		RequestID:           req.Request.RequestID,
		Preview:             preview,
		RiskClass:           risk.Class(req.Request.RiskClass),
		DualControlRequired: req.Request.DualControlRequired,
	}
	legs := make([]integration.ApprovalLeg, 0, len(req.Legs))
	for _, lw := range req.Legs {
		challenge, cerr := base64.StdEncoding.DecodeString(lw.Challenge)
		if cerr != nil {
			writeError(w, http.StatusBadRequest, "challenge invalido")
			return
		}
		sig, serr := base64.StdEncoding.DecodeString(lw.Signature)
		if serr != nil {
			writeError(w, http.StatusBadRequest, "assinatura invalida")
			return
		}
		// O par de attestation em base64 MALFORMADO é 400 — coerente com
		// challenge/signature/preview — para NÃO virar um fail-open silencioso (entregar nil
		// ao gate em vez de rejeitar) quando a porta de verificação está ligada: nil seria
		// indistinguível de "não enviou attestation" e mudaria o erro de "inválida" para
		// "em falta", perdendo a atribuição.
		var attest []byte
		if lw.DeviceAttestation != "" {
			decoded, aerr := base64.StdEncoding.DecodeString(lw.DeviceAttestation)
			if aerr != nil {
				writeError(w, http.StatusBadRequest, "device_attestation invalido")
				return
			}
			attest = decoded
		}
		var clientData []byte
		if lw.DeviceClientData != "" {
			decoded, aerr := base64.StdEncoding.DecodeString(lw.DeviceClientData)
			if aerr != nil {
				writeError(w, http.StatusBadRequest, "device_client_data invalido")
				return
			}
			clientData = decoded
		}
		legs = append(legs, integration.ApprovalLeg{
			Approver:          lw.Approver,
			Session:           lw.Session,
			Credential:        lw.Credential,
			Challenge:         challenge,
			DeviceAttestation: attest,
			DeviceClientData:  clientData,
			Signature:         sig,
		})
	}

	// BRIDGE DE APROVAÇÃO (AOS-021): quando o broker está composto, a cerimónia produz um
	// GRANT PERSISTIDO amarrado à preview — é o que a retoma apresenta ao Reference Monitor
	// para destravar a acção escalada. Antes disto o /approve só verificava e respondia
	// "authorized": a aprovação EVAPORAVA e nada era destravado.
	//
	// O id do grant é o request_id do pedido: já é o âmbito anti-replay dos challenges, e
	// mantém o broker sem fontes de aleatoriedade (determinista e reproduzível).
	if h.node.ApprovalBroker != nil {
		grant, gerr := h.node.ApprovalBroker.Approve(r.Context(), feReq.RequestID, feReq, legs...)
		if gerr != nil {
			// Fail-closed e resposta UNIFORME: não se revela qual invariante falhou (o audit
			// tem o erro dedicado).
			writeError(w, http.StatusForbidden, "aprovacao recusada")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "authorized",
			"approvers": grant.Approvers,
			// grant_id é a EVIDÊNCIA que destrava a acção na retoma; expires_at é a janela
			// (o grant é de USO-ÚNICO e expira — ver integration.DefaultApprovalTTL).
			"grant_id":   grant.ID,
			"expires_at": grant.ExpiresAt.UTC().Format(time.RFC3339),
		})
		return
	}

	decision, err := h.node.FourEyes.Authorize(r.Context(), feReq, legs...)
	if err != nil || !decision.Authorized {
		// Fail-closed: qualquer negação ⇒ 403, sem revelar QUAL invariante falhou (o audit
		// tem o erro dedicado; a resposta HTTP é uniforme).
		writeError(w, http.StatusForbidden, "aprovacao recusada")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "authorized", "approvers": decision.Approvers})
}

// ---------------------------------------------------------------------------
// Utilitários de request/response
// ---------------------------------------------------------------------------

// admitControl aplica a ADMISSION do PLANO DE CONTROLO (bucket dedicado) ANTES de qualquer
// descodificação de corpo ou verificação de assinatura. Devolve false (e escreve 429)
// quando o bucket está vazio — sem isto, um atacante inundaria /steer,/pause,/approve a
// taxa ilimitada, forçando um decode (até maxBodyBytes) e um ed25519.Verify por pedido
// (exaustão CPU no canal trusted). O limite de corpo continua a ser imposto em decodeJSON.
func (h *apiHandler) admitControl(w http.ResponseWriter) bool {
	if !h.ctrlBucket.allow() {
		writeError(w, http.StatusTooManyRequests, "rate limit excedido")
		return false
	}
	return true
}

// admitControlMTLS impõe o mTLS do PLANO DE CONTROLO (DEF-012, EIXO 1) quando composto: a rota
// de controlo RECUSA (403) se não houver um certificado de cliente VERIFICADO contra a CA montada.
// É a PRIMEIRA barreira (transporte); a assinatura ed25519 do corpo (AOS-160) é imposta A SEGUIR
// pelo handler — pelo que esta função é ADITIVA e NUNCA um bypass: um certificado válido com
// assinatura ausente/má é aceite AQUI mas continua RECUSADO pela verificação ed25519 subsequente.
//
// Quando o mTLS de controlo está DESLIGADO (o comportamento actual), devolve sempre true — a
// única autenticação continua a ser a ed25519, exactamente como antes de DEF-012.
//
// O predicado é `len(r.TLS.VerifiedChains) > 0`: com tls.VerifyClientCertIfGiven no listener, uma
// cadeia verificada só existe se o cliente APRESENTOU um certificado E ele encadeou até à CA
// montada (um certificado apresentado mas não-encadeável falha o handshake e nem chega aqui; um
// pedido sem certificado chega com VerifiedChains vazio ⇒ recusado). A resposta é uniforme
// (não-enumerável): só o 403 distingue, sem revelar o estado do run.
func (h *apiHandler) admitControlMTLS(w http.ResponseWriter, r *http.Request) bool {
	if !h.controlMTLS {
		return true
	}
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		writeError(w, http.StatusForbidden, "certificado de cliente exigido")
		return false
	}
	return true
}

// decodeJSON aplica o LIMITE DE CORPO ([http.MaxBytesReader]) e descodifica JSON. Devolve
// (status, ok): ok=false com 413 quando o corpo excede o limite (achado nº5), ou 400 num
// JSON malformado. O corpo limitado protege o ingresso de ser vector de exaustão.
func (h *apiHandler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) (int, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return http.StatusRequestEntityTooLarge, false
		}
		return http.StatusBadRequest, false
	}
	return http.StatusOK, true
}

// writeJSON serializa v como resposta JSON com o status dado.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError devolve uma resposta de erro UNIFORME (mensagem sem detalhe enumerável). Só o
// status distingue as classes de falha; o corpo nunca vaza a existência de um run alheio.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// Token bucket — admission de POST /runs
// ---------------------------------------------------------------------------

// tokenBucket é um limitador token-bucket clássico com relógio injectável. Concorrente-seguro.
type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	capacity   float64
	refillRate float64 // tokens por segundo
	last       time.Time
	now        func() time.Time
}

// newTokenBucket constrói um bucket cheio (capacity tokens) que reabastece a refillRate
// tokens/segundo. capacity <= 0 desliga de facto o limite (o allow devolve sempre true).
func newTokenBucket(capacity, refillRate float64, now func() time.Time) *tokenBucket {
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{
		tokens:     capacity,
		capacity:   capacity,
		refillRate: refillRate,
		last:       now(),
		now:        now,
	}
}

// allow consome um token se houver; devolve false quando o bucket está vazio (⇒ 429). Um
// bucket de capacidade <= 0 é interpretado como SEM limite (sempre permite).
func (b *tokenBucket) allow() bool {
	if b.capacity <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// APIServer — http.Server com bind-guardrail
// ---------------------------------------------------------------------------

// APIServer embrulha o http.Handler do nó num http.Server endurecido (timeouts anti
// slowloris) e impõe o BIND-GUARDRAIL: [Serve] recusa fazer bind a um endereço não-loopback
// enquanto a autenticação do canal de controlo não estiver ligada.
type APIServer struct {
	node *Node
	http *http.Server
	logw io.Writer
	// tlsEnabled é true quando o nó TERMINA TLS (o par foi carregado em s.http.TLSConfig);
	// externalTLS é true quando a terminação foi DECLARADA a montante. Qualquer um satisfaz a
	// quarta conjunção do bind-guardrail (transporte cifrado). Ver [APIServer.transportEncrypted].
	tlsEnabled  bool
	externalTLS bool
	// controlMTLS é true quando o mTLS do plano de controlo (DEF-012, EIXO 1) foi composto: o
	// listener verifica o certificado de cliente (VerifyClientCertIfGiven) e os handlers de
	// controlo recusam sem cadeia verificada. Exposto para diagnóstico/banner.
	controlMTLS bool
}

// NewAPIServer compõe o handler ([NewAPIHandler]) e um http.Server com timeouts endurecidos.
// Quando a terminação TLS no nó está composta ([WithTLSFiles]), carrega o par certificado+chave
// dos ficheiros montados e monta uma [tls.Config] ENDURECIDA — fail-closed: um par inválido ou
// uma config TLS incompleta abortam ([ErrBadTLSKeyPair]/[ErrIncompleteTLSConfig]).
func NewAPIServer(svc *NodeService, node *Node, opts ...APIOption) (*APIServer, error) {
	handler, err := NewAPIHandler(svc, node, opts...)
	if err != nil {
		return nil, err
	}
	var cfg apiConfig
	for _, o := range opts {
		o(&cfg)
	}
	// O WriteTimeout do http.Server é um deadline POR-LIGAÇÃO (anti slowloris). A rota de
	// trajectória SSE anula-o para si própria (transporte fail-safe); os restantes handlers
	// continuam protegidos por ele. Configurável (WithServerWriteTimeout) para testes.
	writeTimeout := DefaultWriteTimeout
	if cfg.serverWriteTO > 0 {
		writeTimeout = cfg.serverWriteTO
	}
	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       DefaultIdleTimeout,
	}

	// TERMINAÇÃO TLS NO NÓ (AOS-209). Precedência sobre a declaração externa: se o nó termina
	// TLS, não há terminação "a montante" a declarar. Fail-closed em três frentes: só um
	// caminho definido aborta; um par que não carrega aborta; ambos vazios ⇒ sem TLS no nó.
	tlsEnabled := false
	externalTLS := cfg.externalTLS
	switch {
	case cfg.tlsCertPath != "" && cfg.tlsKeyPath != "":
		cert, cerr := tls.LoadX509KeyPair(cfg.tlsCertPath, cfg.tlsKeyPath)
		if cerr != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadTLSKeyPair, cerr)
		}
		httpSrv.TLSConfig = hardenedServerTLSConfig(cert)
		tlsEnabled = true
		externalTLS = false // o nó termina TLS: a declaração externa é irrelevante.
	case cfg.tlsCertPath != "" || cfg.tlsKeyPath != "":
		return nil, ErrIncompleteTLSConfig
	}

	// mTLS DO PLANO DE CONTROLO (DEF-012, EIXO 1). OPT-IN e ADITIVO. Exige terminação TLS NO nó
	// (a autenticação mútua é do handshake) e liga tls.VerifyClientCertIfGiven + ClientCAs no
	// listener — ESCOPADO: verifica o certificado SE apresentado, e são os handlers de controlo
	// (não o listener) que RECUSAM quando falta. Fail-closed de config: CA ilegível/inválida
	// aborta ([ErrBadControlMTLSCA]); sem TLS no nó aborta ([ErrControlMTLSNeedsNodeTLS]).
	controlMTLS := false
	if cfg.controlMTLSCAPath != "" {
		if !tlsEnabled {
			return nil, ErrControlMTLSNeedsNodeTLS
		}
		pool, cerr := loadClientCAPool(cfg.controlMTLSCAPath)
		if cerr != nil {
			return nil, cerr
		}
		httpSrv.TLSConfig.ClientCAs = pool
		httpSrv.TLSConfig.ClientAuth = tls.VerifyClientCertIfGiven
		controlMTLS = true
	}

	return &APIServer{
		node:        node,
		http:        httpSrv,
		logw:        cfg.logw,
		tlsEnabled:  tlsEnabled,
		externalTLS: externalTLS,
		controlMTLS: controlMTLS,
	}, nil
}

// loadClientCAPool lê o bundle PEM da CA de CLIENTE montado (DEF-012, EIXO 1) e constrói o pool de
// confiança que o listener usa para VERIFICAR o certificado de cliente do plano de controlo.
// Fail-closed: ficheiro ilegível ou sem nenhum certificado PEM válido ⇒ [ErrBadControlMTLSCA]. Só
// material PÚBLICO (a CA); nenhuma chave privada entra por aqui.
func loadClientCAPool(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler %q: %v", ErrBadControlMTLSCA, path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%w: %q sem certificado PEM de CA valido", ErrBadControlMTLSCA, path)
	}
	return pool, nil
}

// controlMTLSEnabled indica se o mTLS do plano de controlo (DEF-012, EIXO 1) foi composto (para o
// diagnóstico/banner do arranque).
func (s *APIServer) controlMTLSEnabled() bool { return s.controlMTLS }

// hardenedServerTLSConfig monta a [tls.Config] endurecida do servidor a partir do par
// certificado+chave carregado (AOS-209): MinVersion TLS 1.2 (recusa SSLv3/TLS1.0/1.1) e um
// conjunto de cipher suites AEAD sobre ECDHE (forward secrecy) para o handshake TLS 1.2 — as
// suites de TLS 1.3 não são configuráveis e são sempre seguras. Só stdlib (crypto/tls).
func hardenedServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	}
}

// Handler expõe o http.Handler subjacente (útil para httptest sem abrir um socket).
func (s *APIServer) Handler() http.Handler { return s.http.Handler }

// controlAuthenticated indica se o canal de SINAIS do nó (steer/pause) está AUTENTICADO **E
// OPERÁVEL**: exige (a) o Ed25519Authenticator de AOS-160 presente, (b) um modo de identidade
// real e (c) PELO MENOS UM operador com pubkey registada. É a condição que o bind-guardrail
// exige para permitir um bind não-loopback.
//
// ÂMBITO DELIBERADO (não é omissão). O predicado NÃO olha para os aprovadores do four-eyes:
// um nó com AOS_APPROVERS_FILE e ZERO operadores tem o /approve operável e é, mesmo assim,
// recusado. Escolheu-se a leitura CONSERVADORA — steer/pause são a superfície de INTERVENÇÃO
// (parar um run a correr), a que justifica expor a porta à rede; alargar a condição a
// "qualquer superfície de controlo utilizável" abriria o bind não-loopback a um nó que não
// consegue receber UM único sinal de paragem. O que muda com AOS-193 é que a DESCRIÇÃO
// (mensagem de erro e README) passa a nomear steer/pause em vez de "canal de controlo", para
// não afirmar mais do que se verifica.
//
// A terceira conjunta é o APERTO de AOS-193 (achado STR-04). Até aqui o predicado era
// VÁCUO no artefacto entregue: [Bootstrap] compõe SEMPRE o SteerAuth e declara sempre um
// IdentityMode real, pelo que (a)∧(b) era IDENTICAMENTE VERDADEIRO em qualquer nó saído do
// composition-root — o guardrail nunca recusava nada e a única forma de o falsificar era
// mutar o Node à mão (foi o que os testes fizeram: `unauth.SteerAuth = nil`). Pior: sem
// caminho de configuração para os operadores (ORF-02), o nó que ele deixava passar tinha o
// canal de controlo em default-deny TOTAL — "autenticado" no sentido em que uma porta
// soldada está trancada.
//
// Com (c), a condição passa a DISCRIMINAR de facto entre dois nós REAIS produzidos pelo mesmo
// Bootstrap (com e sem AOS_OPERATORS) e passa a coincidir com o que deploy/node/README.md
// sempre prometeu: «identidade real + operadores». A cardinalidade é lida do PRÓPRIO
// authenticator ([integration.Ed25519Authenticator.EmitterCount]) — o estado que decide os
// 403 — e não de uma cópia da config que poderia divergir dele.
//
// MUDANÇA DE COMPORTAMENTO (documentada em deploy/node/README.md): um nó que hoje faz bind a
// 0.0.0.0 sem operadores passa a RECUSAR arrancar com [ErrRefuseNonLoopbackBind]. É o
// comportamento correcto — expor à rede um plano de controlo que não consegue aceitar UM
// único sinal legítimo dá todo o risco (superfície exposta) e nenhum benefício.
func (s *APIServer) controlAuthenticated() bool {
	if s.node == nil || s.node.SteerAuth == nil {
		return false
	}
	if s.node.IdentityMode != IdentityModeReal && s.node.IdentityMode != IdentityModeRealHardened {
		return false
	}
	return s.node.SteerAuth.EmitterCount() > 0
}

// operatorCount devolve o nº de operadores com pubkey registada no canal de controlo (0
// quando o nó/authenticator não existem). Serve o DIAGNÓSTICO do guardrail — nunca expõe IDs
// nem material de chave.
func (s *APIServer) operatorCount() int {
	if s.node == nil || s.node.SteerAuth == nil {
		return 0
	}
	return s.node.SteerAuth.EmitterCount()
}

// identityMode devolve o modo de identidade do nó (vazio se não houver nó), para o
// diagnóstico do guardrail.
func (s *APIServer) identityMode() string {
	if s.node == nil {
		return ""
	}
	return s.node.IdentityMode
}

// Serve faz bind a addr e serve, APÓS o bind-guardrail. RECUSA (devolve
// [ErrRefuseNonLoopbackBind], nunca um mero log) fazer bind a um endereço não-loopback
// quando o canal de controlo não está autenticado — o Listen nem sequer acontece. Loopback
// (127.0.0.1/::1/localhost) é sempre permitido.
func (s *APIServer) Serve(addr string) error {
	ln, err := s.listen(addr)
	if err != nil {
		return err
	}
	return s.serveListener(ln)
}

// serveListener serve o listener já aberto, escolhendo TLS ou texto-claro conforme a
// terminação composta no nó (AOS-209). Extraído de [APIServer.Serve] para os testes poderem
// abrir o listener (via [APIServer.listen], que aplica os guardrails) e servir sobre a MESMA
// via de produção, sem reimplementar a escolha TLS/claro.
func (s *APIServer) serveListener(ln net.Listener) error {
	if s.http.TLSConfig != nil {
		// O par certificado+chave já vive em s.http.TLSConfig.Certificates (carregado em
		// NewAPIServer); os ficheiros vazios dizem ao ServeTLS para o usar.
		return s.http.ServeTLS(ln, "", "")
	}
	return s.http.Serve(ln)
}

// transportEncrypted é a QUARTA conjunção do bind-guardrail (AOS-209): o transporte do
// ingresso está cifrado, seja porque o NÓ termina TLS (tlsEnabled), seja porque a terminação
// foi DECLARADA a montante (externalTLS). Qualquer um satisfaz a condição; nenhum ⇒ texto-claro.
func (s *APIServer) transportEncrypted() bool {
	return s.tlsEnabled || s.externalTLS
}

// listen aplica o bind-guardrail e, se passar, abre o listener TCP. Extraído para ser
// testável sem entrar no laço de serviço bloqueante. São DUAS conjunções sobre o mesmo eixo
// (bind não-loopback), verificadas por ordem: primeiro o canal de controlo autenticado
// (AOS-193), depois o transporte cifrado (AOS-209). Loopback passa ambas.
func (s *APIServer) listen(addr string) (net.Listener, error) {
	if err := guardBind(addr, s.controlAuthenticated()); err != nil {
		// O log nomeia a CARDINALIDADE de operadores: é o discriminante que AOS-193 acrescentou
		// e a causa esmagadoramente mais provável da recusa num nó de produção (identidade real
		// + SteerAuth composto vêm sempre do Bootstrap).
		s.log("bind-guardrail RECUSOU %q: %v (modo de identidade=%q, operadores registados=%d)",
			addr, err, s.identityMode(), s.operatorCount())
		return nil, err
	}
	if err := guardCleartext(addr, s.transportEncrypted()); err != nil {
		s.log("bind-guardrail RECUSOU %q em TEXTO-CLARO: %v (termine TLS no no com AOS_TLS_CERT_PATH/AOS_TLS_KEY_PATH, ou declare AOS_TLS_EXTERNAL_TERMINATION=1)",
			addr, err)
		return nil, err
	}
	return net.Listen("tcp", addr)
}

// Shutdown encerra o http.Server graciosamente.
func (s *APIServer) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// log escreve no destino de logs da API (se configurado).
func (s *APIServer) log(format string, args ...any) {
	if s.logw != nil {
		fmt.Fprintf(s.logw, "[aos-api] "+format+"\n", args...)
	}
}

// guardBind é o CORAÇÃO do bind-guardrail (função pura, testável): devolve
// [ErrRefuseNonLoopbackBind] quando addr NÃO é loopback e o canal de controlo NÃO está
// autenticado. Um bind loopback é sempre permitido (o canal de controlo só é alcançável do
// próprio host); um bind não-loopback sem authn é RECUSADO — expor o canal de controlo à
// rede sem autenticação é o achado nº4.
func guardBind(addr string, controlAuthenticated bool) error {
	if controlAuthenticated {
		return nil // authn ligada: qualquer bind é permitido
	}
	if isLoopbackAddr(addr) {
		return nil // loopback: só o próprio host alcança o canal de controlo
	}
	return fmt.Errorf("%w: addr=%q", ErrRefuseNonLoopbackBind, addr)
}

// guardCleartext é a QUARTA conjunção do bind-guardrail (função pura, testável, AOS-209):
// devolve [ErrRefuseCleartextBind] quando addr NÃO é loopback e o transporte NÃO está cifrado.
// Espelha EXACTAMENTE a disciplina de [guardBind] (mesma forma, mesmo tratamento conservador
// de loopback) — não é um mecanismo novo, é a mesma barreira sobre um segundo eixo: um bind
// não-loopback em texto-claro expõe API/SSE/DSAR à leitura de qualquer intermediário.
func guardCleartext(addr string, transportEncrypted bool) error {
	if transportEncrypted {
		return nil // TLS no nó ou declarado a montante: o transporte é cifrado
	}
	if isLoopbackAddr(addr) {
		return nil // loopback: só o próprio host alcança a porta, sem intermediário na rota
	}
	return fmt.Errorf("%w: addr=%q", ErrRefuseCleartextBind, addr)
}

// isLoopbackAddr decide, CONSERVADORAMENTE, se addr faz bind SÓ à interface de loopback. Um
// host vazio (ex.: ":8080" ⇒ todas as interfaces), um IP não-loopback, ou um hostname que
// não se consegue confirmar como loopback são tratados como NÃO-loopback (fail-closed: na
// dúvida, o guardrail recusa). "localhost" e os IPs de loopback (127.0.0.0/8, ::1) são
// loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // pode ter vindo sem porta
	}
	if host == "" {
		return false // bind a todas as interfaces ⇒ NÃO-loopback
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // hostname não confirmável ⇒ conservador (não-loopback)
	}
	return ip.IsLoopback()
}

// ---------------------------------------------------------------------------
// RETOMA de um run suspenso (AOS-021) — POST /runs/{id}/resume
// ---------------------------------------------------------------------------

// resumeRequest transporta a credencial NHI FRESCA com que o run é retomado. A credencial
// original NÃO foi persistida (é um bearer token, e teria expirado durante a janela de
// aprovação) — quem retoma re-autentica-se.
type resumeRequest struct {
	Credential string `json:"credential"`
}

// handleResume retoma um run SUSPENSO à espera de aval humano. É plano de CONTROLO: passa
// pela admissão e pelo mTLS de controlo como /steer, /pause e /approve.
//
// A autorização REAL da acção retomada não acontece aqui — acontece onde sempre aconteceu:
// a cadeia de mediação COMPLETA volta a correr sobre a credencial fresca (identidade,
// revalidação, PDP, taint, escopo, egress). Este endpoint só reconstitui e re-submete.
func (h *apiHandler) handleResume(w http.ResponseWriter, r *http.Request) {
	if !h.admitControl(w) {
		return
	}
	if !h.admitControlMTLS(w, r) {
		return
	}
	runID := r.PathValue("id")
	if runID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req resumeRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	if strings.TrimSpace(req.Credential) == "" {
		// Fail-closed: sem credencial fresca não há retoma. Aceitar vazio faria o run
		// prosseguir anonimamente até o hook de identidade o negar — mais tarde e com
		// pior diagnóstico.
		writeError(w, http.StatusBadRequest, "credencial de retoma em falta (a original nao e persistida — re-autentique)")
		return
	}
	err := h.svc.Resume(r.Context(), runID, req.Credential)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "resumed"})
	case errors.Is(err, ErrRunNotSuspended):
		// 404 UNIFORME (como o resto do read-path): não se distingue "não existe" de
		// "existe mas não está suspenso" — não enumera runs alheios.
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrResumeUnavailable):
		writeError(w, http.StatusNotImplemented, "retoma indisponivel (four-eyes nao composto)")
	case errors.Is(err, ErrNoResumeRecord):
		writeError(w, http.StatusConflict, "run sem registo de retoma — nao e reconstituivel")
	default:
		// Um 500 SEM diagnóstico é indepurável: a resposta ao cliente mantém-se opaca
		// (não revela interno), mas o operador tem de conseguir ver a causa no log.
		h.svc.log("retoma do run %q falhou: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "retoma falhou")
	}
}
