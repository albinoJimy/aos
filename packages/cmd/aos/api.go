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
	"encoding/hex"
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
	"github.com/aos-ref/kernel/agent-runtime/state"
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
	maxBodyBytes   int64
	rateBurst      float64
	ratePerSec     float64
	ctrlRateBurst  float64
	ctrlRatePerSec float64
	maxInFlight    int
	// maxTurnsCeiling é o TECTO node-local do nº de turnos de um run (AOS-203, achado F2 do
	// desafio A5). Um `max_turns` do corpo de POST /runs é CLAMPADO a este valor na fronteira
	// de INGRESSO ANTES de construir o Goal, para que o submissor não escolha um orçamento de
	// turnos arbitrário: sem tecto, um único `POST /runs {max_turns: 200}` esgota o LimitRPM do
	// keypool do gateway e deixa o nó incapaz de chamar o modelo até reiniciar (o orçamento RPM
	// é do PROCESSO, não do run). Default [agentruntime.DefaultMaxTurns]; endurecível por
	// AOS_MAX_TURNS via [WithMaxTurnsCeiling]. É node-local: não importa nada do escalonador.
	maxTurnsCeiling  int
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

// WithMaxTurnsCeiling define o TECTO node-local do nº de turnos de um run (AOS-203, F2). Um
// `max_turns` do corpo de POST /runs maior do que este tecto — ou ausente — é CLAMPADO a ele
// na fronteira de ingresso, ANTES de o Goal chegar ao loop. Fecha o DoS de um pedido em que
// um `max_turns` grande esgota o orçamento RPM do gateway (que é do processo, não do run) e
// deixa o nó sem poder chamar o modelo. Default [agentruntime.DefaultMaxTurns]; n <= 0 é
// ignorado (mantém o default) — o tecto NUNCA se desliga: um clamp "desligável" reabriria o
// próprio buraco que fecha.
func WithMaxTurnsCeiling(n int) APIOption {
	return func(c *apiConfig) {
		if n > 0 {
			c.maxTurnsCeiling = n
		}
	}
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
	// O guard que serializa as passagens do [audit.ExpirationJob] vive em
	// [NodeService.expireInFlight] — NÃO aqui. Mudou de sítio em AOS-267, quando o scheduler
	// interno passou a conduzir a MESMA passagem: um guard no handler só excluiria as
	// invocações da rota entre si, e a rota voltaria a poder correr em simultâneo com o tick.
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
		maxTurnsCeiling:  agentruntime.DefaultMaxTurns,
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
	// O MESMO registo de saude do servico: as tres vias de selagem partilham a cadeia, logo
	// partilham o desfecho. Ver [saudeDeSelagem].
	if readGov != nil && svc != nil {
		readGov.saude = &svc.seloWORM
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
	if err := registar(mux, h, h.tabelaDeRotas()); err != nil {
		return nil, err
	}

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
	// Região do submissor, retida para a decisão de colisão de run_id mais abaixo. Vazia ⇒ modo
	// legado (sem gate soberano), onde a colisão continua a responder 201 uniforme.
	var submitterRegion string
	if h.readGov != nil {
		submitter, ok := h.readGov.authorize(r)
		if !ok {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		submitterRegion = submitter.region
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

	// TECTO DE TURNOS na fronteira de INGRESSO (AOS-203, achado F2 do desafio A5). O `max_turns`
	// vem do corpo do submissor e, sem clamp, é copiado cru para o Goal — o loop só aplica o
	// default quando o campo é <= 0, pelo que um valor grande passa tal e qual. Composto com o
	// orçamento RPM POR-PROCESSO do keypool do gateway, um único `POST /runs {max_turns: 200}`
	// esgota-o e deixa o nó incapaz de chamar o modelo até reiniciar — um DoS de UM pedido que
	// o rate-limit de ingresso (1 pedido, abaixo do burst) e o tecto de in-flight (1 run) não
	// apanham. O clamp fecha-o aqui, node-local, ANTES de o Goal existir: um `max_turns` acima
	// do tecto — ou ausente (<= 0) — é reduzido ao tecto (default [agentruntime.DefaultMaxTurns],
	// endurecível por AOS_MAX_TURNS). Um valor legítimo abaixo do tecto passa intacto.
	if req.MaxTurns <= 0 || req.MaxTurns > h.cfg.maxTurnsCeiling {
		req.MaxTurns = h.cfg.maxTurnsCeiling
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
		// COLISÃO DE run_id COM CHAMADOR AUTENTICADO (achado A2 da auditoria de 2026-08-17).
		//
		// O 201 uniforme abaixo existe para não ser um ORÁCULO DE EXISTÊNCIA a um chamador
		// ANÓNIMO — a premissa de ADR-016, quando o plano de dados não autenticava. Sob
		// credencial FORTE composta essa premissa deixa de valer: `handleSubmit` já recusou com
		// 403 quem não se autenticou, e o oráculo que o 201 protege não tem a quem revelar.
		//
		// Fica só o custo, e ele é real: um chamador legítimo que colida num run_id recebe
		// "accepted", PERDE a submissão em silêncio, e ao consultar o resultado lê o run de
		// OUTRA pessoa. Foi observado.
		//
		// A condição é `pode LER este run`, não `está autenticado`: um 409 a quem o GET esconde
		// abriria por POST a porta que [readGovernance.authorizeRead] fecha — a existência de um
		// run de OUTRA REGIÃO. Daí exigir residência selada E coincidente. Compara-se contra o
		// `submitterRegion` já resolvido acima, e não se re-verifica a credencial: uma segunda
		// verificação consumiria o `jti` e transformaria a resposta num falso replay.
		if isIdempotentResubmit(err) && h.readGov != nil && h.readGov.cred != nil && submitterRegion != "" {
			if regiao, selada, rerr := h.readGov.runResidency(r.Context(), req.RunID); rerr == nil && selada && regiao == submitterRegion {
				writeError(w, http.StatusConflict, "run_id ja existe")
				return
			}
		}
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
	Status     string `json:"status"` // "in_progress" | "waiting_on_human" | "completed" | desfecho durável (AOS-252): "failed" | "timed_out" | "killed" | "paused"
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
	// PendingExhaustion são os PROMPTS DE EXAUSTÃO DE ORÇAMENTO por responder (AOS-263) — a
	// segunda decisão humana que suspende um run. CAMPO PRÓPRIO, e não mais uma entrada em
	// PendingApprovals: um prompt de exaustão não tem preview, e apresentá-lo como aprovação
	// daria ao operador um digest de coisa nenhuma para assinar. Ausente quando não há
	// nenhum por responder (ou a maquinaria HITL não está composta).
	PendingExhaustion []pendingExhaustionWire `json:"pending_exhaustion,omitempty"`
	// PendingUnavailable declara que as duas listas acima saíram VAZIAS por não terem podido
	// ser lidas — não por não haver nada. Sem este campo, uma falha de leitura era
	// indistinguível de «nada por decidir», e num run suspenso por AOS-263 essa ambiguidade
	// custa caro: o operador vê `waiting_on_human` sem pergunta e não sabe se espera ou se
	// investiga. Ausente no caminho normal (é a leitura que degrada, não a resposta).
	PendingUnavailable bool `json:"pending_unavailable,omitempty"`
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

// pendingExhaustionWire é a face de wire de um PROMPT DE EXAUSTÃO por responder (AOS-263).
//
// NÃO tem preview e nunca terá: no tipo aprovação a preview é a amarra criptográfica do
// EFEITO exibido (WYSIWYS); aqui não há efeito nenhum a autorizar — a pergunta é sobre o
// RUN, e a sua amarra são os números que a justificam (limiar, consumido, tecto), lidos do
// aviso de burn-down de AOS-262 e não recalculados.
//
// As OPÇÕES são as que TÊM executor, e só essas: `continue` e `abort` (AOS-263 parte 3), ambas
// na MESMA rota assinada — as duas metades da pergunta, com a mesma autoridade. A RETOMA não
// está entre elas: é a execução que se segue a um `continue` selado, e enquanto a pergunta
// estiver por responder a própria rota de retoma recusa. NÃO aparecem `extend` — decisão do
// dono ((iii), 2026-08-12): o tecto não tem mutador e os limites são configuração fora do log
// de eventos — nem `summarize_stop`, que não tem caminho de resumo no loop e seria uma paragem
// comum com um nome que promete mais. Ambas ficam DECLARADAS (em exhaustion_decision.go, no
// banner de arranque e no README do nó) e NENHUMA viaja no wire: uma lista de "não-oferecidas"
// dentro da resposta convidaria um cliente a tentá-las. Oferecer uma escolha que ninguém
// consegue executar é a mentira que AOS-262 se recusou a contar — e seria pior aqui, onde o run
// está parado à espera dela.
type pendingExhaustionWire struct {
	StepID string `json:"step_id"`
	Turn   int    `json:"turn"`
	// Threshold/ConsumedTokens/LimitTokens são o PORQUÊ da pergunta, nos termos em que o
	// burn-down a produziu. Os tokens são um LIMITE INFERIOR do consumo do run: o ledger de
	// turnos conta os TURNOS DE MODELO e não pesa tool calls (AOS-261).
	Threshold      float64 `json:"threshold"`
	ConsumedTokens int64   `json:"consumed_tokens"`
	LimitTokens    int64   `json:"limit_tokens"`
	// A MESMA amarra na dimensão $ (micro-USD INTEIRO), presente só quando o nó tem tecto em
	// dólares configurado (`AOS_BUDGET_MAX_COST_MICRO_USD`). Sem ela, um operador cujo run foi
	// bloqueado pelo tecto em `$` lia números de TOKENS que contradiziam a razão da paragem.
	ConsumedCostMicroUSD int64 `json:"consumed_cost_micro_usd,omitempty"`
	LimitCostMicroUSD    int64 `json:"limit_cost_micro_usd,omitempty"`
	// Reason é a RAZÃO da pergunta — a grandeza que a levantou, nas palavras de quem a
	// levantou (limiar de burn-down cruzado vs tecto ATINGIDO, com os pares consumido/tecto lá
	// dentro). É o campo que torna a pergunta decidível sem cruzar com o log do processo.
	Reason string `json:"reason,omitempty"`
	// CreatedAt é a âncora do TTL — a partir daqui o varrimento de pendentes conta a idade
	// da pergunta (RFC3339). Ausente só num registo escrito sem relógio (nunca expira).
	CreatedAt string `json:"created_at,omitempty"`
	// Options são as decisões EXECUTÁVEIS, cada uma com a rota que a executa.
	Options []exhaustionOptionWire `json:"options"`
}

// exhaustionOptionWire é uma opção do prompt e a ROTA que a executa. A rota viaja com a
// opção de propósito: uma opção sem rota é indistinguível de uma promessa.
type exhaustionOptionWire struct {
	ID    string `json:"id"`
	Route string `json:"route"`
}

// exhaustionOptions são as opções apresentadas. É uma função e não uma variável global para
// não haver uma fatia partilhada que um handler pudesse mutar por engano.
//
// AS DUAS TÊM EXECUTOR — e o MESMO, que é o ponto: `continue` e `abort` são as duas metades
// da pergunta e entram ambas por `POST /runs/{id}/exhaustion` (AOS-263 parte 3), com a mesma
// assinatura de operador pinado e o mesmo selo WORM. Uma resposta «deixa correr» mais barata
// de dar do que «pára» tornaria o prompt contornável precisamente na direcção arriscada.
//
// A RETOMA (`POST /runs/{id}/resume`) NÃO está nesta lista e não é uma opção: é a EXECUÇÃO que
// se segue a um `continue` selado, e enquanto a pergunta estiver por responder a própria rota
// recusa-a. Ficam de fora, DECLARADAS em exhaustion_decision.go, no banner e no README:
// `extend` (o tecto não tem mutador — decisão do dono (iii)) e `summarize_stop` (o loop não tem
// caminho de resumo). Apresentar uma escolha que ninguém executa é prometer o que não existe —
// e aqui seria pior do que em AOS-262, porque o run está PARADO à espera da resposta.
func exhaustionOptions() []exhaustionOptionWire {
	return []exhaustionOptionWire{
		{ID: exhaustionOptionContinue, Route: exhaustionDecisionRoute},
		{ID: exhaustionOptionAbort, Route: exhaustionDecisionRoute},
	}
}

// pendingApprovalsFor resolve as aprovações pendentes de um run para a resposta. Devolve
// nil quando o four-eyes não está composto ou nada está por decidir. Uma falha de leitura
// NÃO quebra a resposta de estado: o pendente é informação de administração, e degradar
// para "sem pendentes" é preferível a negar a consulta do estado do run (o operador
// percebe pela ausência; o run continua suspenso e nada executa).
func (h *apiHandler) pendingApprovalsFor(ctx context.Context, runID string) []pendingApprovalWire {
	aprovacoes, _, _ := h.pendingFor(ctx, runID)
	return aprovacoes
}

// pendingFor lê UMA VEZ o registo de pendentes do run e separa-o pelas DUAS faces de wire —
// aprovações (AOS-021) e prompts de exaustão (AOS-263). Uma leitura só: o registo é um
// stream partilhado e este caminho é POLLING do operador; duas travessias por resposta
// duplicariam o custo do endpoint sem acrescentar informação.
//
// A degradação é a de sempre: uma falha de leitura NÃO quebra a resposta de estado — mas
// deixa de ser MUDA. O terceiro valor de retorno diz que a lista saiu por indisponibilidade e
// não por vazio, e o chamador declara-o no wire (`pending_unavailable`).
//
// PORQUE PASSOU A IMPORTAR (AOS-263): numa aprovação, a ausência lê-se como «nada por fazer»
// — o run está parado de qualquer modo e nada executa. Num prompt de exaustão a ausência é
// ambígua e cara: o operador vê um run em `waiting_on_human` SEM pergunta nenhuma, e não tem
// como distinguir uma falha transitória de leitura de uma pergunta que se perdeu. As duas
// pedem acções opostas (esperar vs. investigar), e adivinhar mal deixa o run parado.
//
// Um tipo que ESTA superfície não saiba renderizar é OMITIDO dos dois lados, nunca
// reinterpretado como o outro (ver [integration.PendingRecord]).
func (h *apiHandler) pendingFor(ctx context.Context, runID string) ([]pendingApprovalWire, []pendingExhaustionWire, bool) {
	if h.node.PendingApprovals == nil {
		// Não é indisponibilidade: este nó não tem registo de pendentes de todo, e o banner de
		// arranque di-lo. Anunciar "indisponível" mandaria investigar uma avaria inexistente.
		return nil, nil, false
	}
	recs, err := h.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil {
		h.svc.log("leitura de pendentes do run %q FALHOU — a resposta de estado sai sem a lista e declara-o em pending_unavailable (uma pergunta por responder pode existir e nao estar a ser mostrada): %v", runID, err)
		return nil, nil, true
	}
	if len(recs) == 0 {
		return nil, nil, false
	}
	var aprovacoes []pendingApprovalWire
	var exaustoes []pendingExhaustionWire
	for _, r := range recs {
		switch r.Kind.Resolved() {
		case integration.PendingKindApproval:
			aprovacoes = append(aprovacoes, pendingApprovalWire{
				StepID:         r.StepID,
				Turn:           r.Turn,
				ToolID:         r.ToolID,
				Capability:     r.Capability,
				ResourceType:   r.ResourceType,
				ResourceValue:  r.ResourceValue,
				ResourceRegion: r.ResourceRegion,
				Preview:        base64.StdEncoding.EncodeToString(r.Preview),
			})
		case integration.PendingKindExhaustion:
			exaustoes = append(exaustoes, pendingExhaustionWire{
				StepID:               r.StepID,
				Turn:                 r.Turn,
				Threshold:            r.Threshold,
				ConsumedTokens:       r.ConsumedTokens,
				LimitTokens:          r.LimitTokens,
				ConsumedCostMicroUSD: r.ConsumedCostMicroUSD,
				LimitCostMicroUSD:    r.LimitCostMicroUSD,
				Reason:               r.Reason,
				CreatedAt:            r.CreatedAt,
				Options:              exhaustionOptions(),
			})
		}
	}
	return aprovacoes, exaustoes, false
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
		// UMA PAUSA SAI COMO PAUSA. Achado C da verificação de funcionamento de 2026-08-23:
		// quando o `suspendedDurably` passou a contar `paused` (PR #139, para o `/resume`
		// funcionar), este ramo passou a APANHAR os runs pausados e a rotulá-los
		// `waiting_on_human` — com a lista de pendentes vazia, porque nunca houve pedido
		// nenhum. O `case state.Paused` do switch mais abaixo ficou INALCANÇÁVEL, e o
		// operador ficou à espera de uma decisão que ninguém lhe pediu.
		//
		// A distinção vem no DESFECHO e não de uma segunda leitura do durável: quem a
		// resolve é [NodeService.suspensaoDuravel], na mesma leitura que decidiu a suspensão.
		// O selo da leitura sensível já foi feito acima, para os DOIS ramos — selá-lo outra
		// vez aqui poria dois registos na cadeia WORM para uma leitura só.
		if oc.Result.Paused {
			writeJSON(w, http.StatusOK, runStateResponse{
				RunID:  runID,
				Status: string(state.Paused),
				Paused: true,
				Turns:  oc.Result.Turns,
			})
			return
		}
		// As DUAS decisões humanas que suspendem um run saem por aqui: a aprovação de uma tool
		// call escalada (AOS-021) e o prompt de exaustão de orçamento (AOS-263). Uma leitura
		// só, cada tipo na sua face de wire.
		aprovacoes, exaustoes, indisponivel := h.pendingFor(r.Context(), runID)
		writeJSON(w, http.StatusOK, runStateResponse{
			RunID:              runID,
			Status:             "waiting_on_human",
			Turns:              oc.Result.Turns,
			PendingApprovals:   aprovacoes,
			PendingExhaustion:  exaustoes,
			PendingUnavailable: indisponivel,
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
		// UM RUN PARADO NÃO ESTÁ COMPLETO — a outra metade do achado C.
		//
		// Este ramo escrevia `"completed"` fixo e, ao mesmo tempo, copiava `Paused` do
		// desfecho: a resposta contradizia-se a si própria (`{"status":"completed",
		// "paused":true}`). Um run parado pelo disjuntor — que chega lá SOZINHO com os
		// defaults, 3 iterações estéreis ou 30 min de wall-clock — lia-se como trabalho
		// FEITO. O operador via "completed", parava de olhar, e o run ficava retomável para
		// sempre sem ninguém o retomar.
		//
		// O desfecho já sabe a verdade: a pausa graciosa põe `Paused`, e o disjuntor põe
		// `Tripped` com o `BreakerTarget` que ELE materializou na máquina durável. Usa-se
		// esse rótulo em vez de o achatar — vale para `paused` e para `timed_out`, que são
		// os dois alvos que o disjuntor produz.
		switch {
		case oc.Result.Paused:
			resp.Status = string(state.Paused)
			resp.Paused = true
		case oc.Result.Tripped && oc.Result.BreakerTarget != "":
			resp.Status = oc.Result.BreakerTarget
			resp.Paused = oc.Result.BreakerTarget == string(state.Paused)
		}
		// Um run TERMINADO pode ter deixado pendentes por decidir (ex.: expirou o TTL e
		// voltou a correr sem a acção). Expô-los mantém o histórico legível ao operador.
		resp.PendingApprovals, resp.PendingExhaustion, resp.PendingUnavailable = h.pendingFor(r.Context(), runID)
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
			// preview a assinar em POST /runs/{id}/approve (AOS-021, polling) — e, com ela, os
			// prompts de exaustão por responder (AOS-263).
			aprovacoes, exaustoes, indisponivel := h.pendingFor(r.Context(), runID)
			writeJSON(w, http.StatusOK, runStateResponse{
				RunID:              runID,
				Status:             "in_progress",
				PendingApprovals:   aprovacoes,
				PendingExhaustion:  exaustoes,
				PendingUnavailable: indisponivel,
			})
			return
		}
	}
	// DESFECHO DURÁVEL (AOS-252): os baldes em memória são um cache que um restart (ou a
	// poda FIFO) esvazia; o log não. Um run que esta réplica já não conhece mas cujo log
	// tem um estado FINAL/SUSPENSO reflecte-o — completo, falhado, timed_out, killed ou
	// paused (trip do breaker) — em vez do 404 de "nunca existiu". Um run em ready/running
	// (crash ou órfão sem desfecho — AOS-253) ou inexistente cai no 404 uniforme: a leitura
	// é um caminho de CONSULTA e um erro de leitura resolve-se pelo lado não-enumerável.
	if st, derr := h.svc.DurableState(r.Context(), runID); derr == nil {
		var resp runStateResponse
		switch st {
		case state.Complete:
			resp = runStateResponse{RunID: runID, Status: "completed", Terminated: true}
		case state.Failed, state.TimedOut, state.Killed:
			resp = runStateResponse{RunID: runID, Status: string(st)}
		case state.Paused:
			resp = runStateResponse{RunID: runID, Status: string(state.Paused), Paused: true}
		}
		if resp.Status != "" {
			if !h.sealSensitiveRead(w, r, reader, residency, runID, capReadOutcome) {
				return
			}
			writeJSON(w, http.StatusOK, resp)
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
	// DEPENDÊNCIA CRÍTICA: a hash-chain do WORM ACEITAR ESCRITAS. Três rotas fail-closed dependem
	// de um `Append` — `POST /runs`, `GET /runs/{id}` e `POST /dsar/hold` — e um nó que as recusa
	// todas não está pronto a servir, por mais que o `/healthz` responda.
	//
	// NÃO SE SONDA A ESCRITA AQUI: escrever no WORM a cada `/readyz` poluiria a cadeia
	// tamper-evident com registos de sonda. Lê-se o desfecho das escritas que REALMENTE
	// aconteceram — ver [saudeDeSelagem.aRecusarEscritas].
	//
	// É a ÚLTIMA selagem que decide, não «alguma vez falhou»: uma selagem bem-sucedida posterior
	// limpa o estado, sem intervenção. Um nó que nunca selou nada não é afectado — o valor-zero é
	// «nunca falhou».
	if h.svc != nil && h.svc.seloWORM.aRecusarEscritas() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready"})
		return
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
	// CAUSA do vermelho, sem revelar titulares (remediação da W6). O `aos_dsar_vault_ready` a 0
	// tem TRÊS causas — Vault selado/inalcançável, token nosso a expirar, destruição de chave por
	// confirmar — e o corpo do /readyz é uniforme de propósito. Sem este contador, uma política
	// Transit sem `deletion_allowed` tirava o nó de rotação PERMANENTEMENTE com um sinal
	// indistinguível do de um token a expirar. É uma CONTAGEM de chaves pendentes (nomes já
	// derivados por sha256): nomeia o eixo, nunca o titular. Só sai quando a custódia sabe
	// responder à pergunta.
	if pend, ok := shredPendingOf(h.node.DSARVault); ok {
		g("aos_dsar_vault_shred_unconfirmed", "Destruicoes de KEK (crypto-shred) por CONFIRMAR na custodia; >0 mantem o no unready e o conteudo pode continuar recuperavel.", "gauge", float64(pend), "")
	}

	// aos_ready espelha o veredito do /readyz — AS QUATRO condições: drain, Event Store,
	// custódia da KEK e a hash-chain do WORM a ACEITAR ESCRITAS. É o SLI de disponibilidade a
	// partir do qual o alerta de SLO dispara.
	//
	// A QUARTA FALTAVA, e é o achado F da verificação de funcionamento de 2026-08-23. Quando
	// a cláusula do WORM entrou no `/readyz`, esta linha não a acompanhou — e o comentário
	// continuou a dizer que "espelha o veredito do /readyz", enumerando as três antigas. O
	// resultado era o pior arranjo possível: um mount remontado `:ro` fazia o nó recusar
	// `POST /runs`, `GET /runs/{id}` e `POST /dsar/hold`, o `/readyz` dava 503 — e este
	// `aos_ready`, que é o que o colector RASPA, continuava a ler 1. O painel ficava verde
	// sobre um nó que não servia nada.
	//
	// RESSALVA HONESTA sobre o "espelha": a sonda da custódia corre aqui com o MESMO prazo de
	// 2 s do `/readyz`, mas o avaliador de SLOs usa 5 s ([sloProbeTimeout]). São o mesmo
	// predicado com prazos diferentes, pelo que num nó sob carga o `/readyz` pode dar 503 por
	// prazo enquanto o SLI ainda mede disponível. Está nomeado em vez de calado.
	wormRecusa := h.svc != nil && h.svc.seloWORM.aRecusarEscritas()
	g("aos_ready", "O no reporta-se pronto no /readyz (1) ou nao (0). Cobre as QUATRO condicoes: drain, Event Store, custodia da KEK e WORM a aceitar escritas.", "gauge", b01(!draining && esHealthy && vaultReady && !wormRecusa), "")

	// AOS-274 — SLOs/ALERTAS avaliados em runtime. É a superfície de EXPOSIÇÃO do produtor
	// (slo_evaluator.go): o que aqui aparece foi calculado sobre dados REAIS do nó (spans
	// emitidos, sonda de prontidão, verificação da hash-chain), não sobre valores injectados.
	// Os `Offenders` (trace_ids) NÃO viajam para aqui de propósito: `/metrics` é
	// não-autenticado e a filosofia não-enumerável do nó vale para ele — o drill-down por trace
	// fica no log estruturado do serviço, junto do runbook.
	h.writeSLOMetrics(&b)

	// Runtime Go (USE): saturação de recursos do processo.
	g("aos_goroutines", "Goroutines em execucao.", "gauge", float64(runtime.NumGoroutine()), "")
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	g("aos_memory_alloc_bytes", "Heap alocado e ainda em uso (bytes).", "gauge", float64(ms.Alloc), "")
	g("aos_memory_sys_bytes", "Memoria obtida do SO (bytes).", "gauge", float64(ms.Sys), "")
	g("aos_gc_cycles_total", "Ciclos de GC completos desde o arranque.", "counter", float64(ms.NumGC), "")

	// VARREDOR DE RETENÇÃO (AOS-267) — «a expiração por TTL está a correr?»
	//
	// Era uma pergunta sem resposta em runtime. O escalonador declara-se no banner de ARRANQUE e
	// depois é invisível. As três coisas que podem estar a acontecer leem-se de maneira diferente
	// e exigem acções diferentes, e por isso são TRÊS séries e não uma:
	//
	//   armed=0                      ⇒ nunca foi armado (política ausente, ou intervalo <= 0)
	//   armed=1, stopped=1           ⇒ PAROU por incidente de integridade da hash-chain, e não
	//                                  volta sozinho: exige investigar o WORM e reiniciar o nó
	//   armed=1, stopped=0, sem age  ⇒ armado, à espera do primeiro tick
	//   armed=1, stopped=0, age alta ⇒ deixou de correr sem o dizer
	//
	// O que deixa de acontecer quando isto morre é o apagamento de dados fora do TTL — uma
	// obrigação com prazo, e a única que não dá sinal nenhum por si mesma.
	if h.svc != nil {
		armado := retentionSchedulerArmed(h.node, h.svc.retentionSweepInterval)
		g("aos_retention_scheduler_armed", "O escalonador de expiracao por TTL esta armado (1) ou nao (0). 0 significa que NADA expira sozinho.",
			"gauge", b01(armado), "")
		if armado {
			g("aos_retention_sweeps_total", "Passagens de expiracao CONCLUIDAS desde o arranque.",
				"counter", float64(h.svc.varrimentosTotal.Load()), "")
			g("aos_retention_scheduler_stopped", "O escalonador PAROU definitivamente por incidente de integridade da hash-chain (1). Nao volta sozinho.",
				"gauge", b01(h.svc.varredorParado.Load()), "")
			// A idade só sai DEPOIS da primeira passagem. Antes disso, emitir 0 diria «acabou de
			// varrer» sobre um nó que ainda não varreu nada — e um nó recém-arrancado ficaria
			// indistinguível de um nó saudável durante a primeira hora.
			if u := h.svc.ultimoVarrimentoUnix.Load(); u > 0 {
				g("aos_retention_last_sweep_age_seconds", "Segundos desde a ultima passagem CONCLUIDA. Acima do dobro de AOS_RETENTION_SWEEP_INTERVAL, a expiracao deixou de correr.",
					"gauge", time.Since(time.Unix(u, 0)).Seconds(), "")
			}
		}
	}

	// SELAGEM NO WORM — o que acontece quando a cadeia deixa de aceitar escritas.
	//
	// TRÊS rotas fail-closed dependem de um `Append` e recusavam com o mesmo 503 uniforme, sem
	// log e sem contador: `POST /runs` (residência), `GET /runs/{id}` (selo de leitura sensível) e
	// `POST /dsar/hold`. Nada mais se movia — o `/readyz` não conhecia o WORM, o
	// `aos_worm_partitions` continuava em 126 porque as partições nascem por run e nenhum run novo
	// entrava, e o SLI de integridade LÊ (um erro de I/O fica SEM AMOSTRA, de propósito).
	//
	// COMO SE LÊ:
	//
	//	failures_total a subir              ⇒ o nó está a recusar escritas AGORA
	//	failures_total > 0, sem last_failure_age ⇒ impossível (a idade sai sempre que houve falha)
	//	sem nenhuma das duas                ⇒ nunca falhou nesta vida do processo
	if h.svc != nil {
		g("aos_worm_seal_failures_total", "Selagens no WORM RECUSADAS nas vias de governacao (residencia de run, leitura sensivel, legal hold) desde o arranque. Cada uma corresponde a uma rota que devolveu 503 ao chamador. POR PROCESSO — um restart repoe.",
			"counter", float64(h.svc.seloWORM.falhas.Load()), "")
		// A idade só sai DEPOIS de haver uma falha. Emitir 0 antes disso diria «acabou de falhar»
		// sobre um nó que nunca falhou.
		if u := h.svc.seloWORM.ultimaFalhaSeg.Load(); u > 0 {
			g("aos_worm_seal_last_failure_age_seconds", "Segundos desde a ultima selagem RECUSADA. Cruzada com aos_worm_seal_failures_total distingue «falhou e recuperou» de «esta a falhar agora».",
				"gauge", time.Since(time.Unix(u, 0)).Seconds(), "")
		}
	}

	// RUNS À ESPERA DE UM HUMANO — a única paragem do nó que é DELIBERADA, e a única que não
	// dava sinal nenhum.
	//
	// A serialização das retomas (achado 1.12 da varredura de 2026-08-21) entrou em produção sem
	// observabilidade: um run preso em `waiting_on_human` que não conseguisse retomar não
	// aparecia em série nenhuma — só se dava por ele quando alguém tentasse retomá-lo e falhasse.
	// Uma guarda sem sinal é uma guarda que ninguém vê falhar.
	//
	// COMO SE LÊ, e as duas séries só dizem alguma coisa juntas:
	//
	//	suspended=0                    ⇒ ninguém espera. É uma afirmação VERDADEIRA, e sai.
	//	suspended>0, idade baixa       ⇒ fluxo normal: houve escalada, o humano vai decidir
	//	suspended>0, idade a CRESCER   ⇒ ou ninguém está a decidir, ou a retoma está a falhar —
	//	                                 e é este o caso que não se via de todo
	//
	// POR RÉPLICA. A suspensão é durável e decidível de qualquer réplica; este balde é o cache
	// em-memória desta. Mede «quantos esperam por mim», não «quantos esperam» — e um restart
	// zera-o sem que a suspensão deixe de ser verdade. Fica dito aqui porque a série sozinha
	// convida à leitura errada.
	if h.svc != nil {
		suspensos, maisAntigo := h.svc.suspensosAgora()
		g("aos_runs_suspended", "Runs A ESPERA DE AVAL HUMANO nesta replica (AOS-021). Nao terminaram: continuam retomaveis por POST /runs/{id}/resume. POR REPLICA — um restart zera a contagem sem que a suspensao deixe de ser verdade.",
			"gauge", float64(suspensos), "")
		// A idade só sai se houver alguém à espera COM carimbo. Emitir 0 com o balde vazio diria
		// «alguém acabou de suspender» sobre um nó onde ninguém espera — e um nó saudável ficaria
		// indistinguível de um nó com uma escalada acabada de acontecer.
		if maisAntigo > 0 {
			g("aos_runs_suspended_oldest_age_seconds", "Segundos desde que o run mais antigo comecou a esperar por um humano. A CRESCER sem a contagem descer significa que ninguem esta a decidir, ou que a retoma esta a falhar.",
				"gauge", time.Since(time.Unix(maisAntigo, 0)).Seconds(), "")
		}
	}

	// ORÇAMENTO (AOS-008/256/257) — a FOLGA entre o tecto e o uso real.
	//
	// O README declara há semanas que «o orçamento está configurado onde nunca morde»: 200 000
	// tokens contra ~1 750 medidos por run. O mecanismo funciona; nesta configuração é protecção
	// que não engata. O problema é isso estar só num parágrafo — o banner declara o tecto em
	// detalhe e lê-se como protecção activa.
	//
	// Estas duas séries dão a FOLGA, e a folga é o que se alerta: `max / pico` muito grande
	// significa um tecto decorativo; perto de 1 significa um tecto que vai começar a suspender
	// runs. Nenhum dos dois extremos se vê no banner.
	if h.node != nil && h.node.orcamento != nil {
		g("aos_budget_max_tokens_per_run", "Tecto de tokens POR RUN em vigor (AOS_BUDGET_MAX_TOKENS).",
			"gauge", float64(h.node.orcamento.MaxTokensPerRun()), "")
		// O pico só sai DEPOIS de um run fechar. Emitir 0 antes disso diria «nenhum run gasta
		// nada», que e a leitura mais errada possivel — a folga apareceria infinita num no que
		// ainda nao mediu coisa nenhuma.
		if pico, visto := h.node.orcamento.PicoDeConsumo(); visto {
			g("aos_budget_run_tokens_peak", "Maior consumo POR RUN (tokens) observado por este processo. Cruzado com aos_budget_max_tokens_per_run da a FOLGA: um racio muito grande e um tecto que nunca morde; perto de 1 e um tecto prestes a suspender runs. Por PROCESSO — um restart repoe-o.",
				"gauge", float64(pico), "")
		}
	}

	// VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072) — a cobertura tem de ser ALERTÁVEL, não só
	// legível no banner de arranque.
	//
	// O QUE ISTO FECHA. A âncora é produzida FORA do nó, por uma tarefa que corre sozinha
	// (`selar-worm.ps1`, cadência diária). Se essa tarefa morrer, nada no nó dá por isso: a
	// verificação continua a passar — o que ela verifica não mudou — e a COBERTURA congela
	// enquanto o WORM continua a crescer. O operador fica a acreditar que o WORM está ancorado.
	//
	// A contagem de partições é LIDA AGORA e não no arranque, de propósito. As partições nascem
	// por run; um valor medido no boot e servido como gauge pareceria vivo e estaria congelado —
	// e faria exactamente o contrário do que esta métrica existe para fazer.
	if h.node != nil && h.node.WORM != nil {
		if lister, ok := h.node.WORM.(interface{ Partitions() []string }); ok {
			g("aos_worm_partitions", "Particoes que a hash-chain do WORM tem AGORA (nascem por run).",
				"gauge", float64(len(lister.Partitions())), "")
		}
	}
	if h.node != nil && h.node.ancora != nil {
		a := h.node.ancora
		g("aos_worm_partitions_anchored", "Particoes cobertas pela ancora assinada que passou no arranque. Comparada com aos_worm_partitions da a COBERTURA — que decai sozinha entre selagens, por desenho.",
			"gauge", float64(len(a.Checkpoints)), "")

		// IDADE — a série que deteta a tarefa de selagem morta, e a única que o faz.
		//
		// O `Timestamp` do checkpoint É ASSINADO (entra no `canonicalCheckpoint`) e, até aqui,
		// nunca era LIDO por ninguém. Uma âncora de há um ano verifica exactamente como a de
		// ontem: a assinatura continua válida e o piso continua satisfeito. O que envelhece não
		// é a validade — é a COBERTURA, e é isso que esta série torna alertável.
		//
		// Usa-se o MAIS RECENTE: todos os checkpoints de uma selagem partilham o instante, e o
		// mais recente é «quando foi a última vez que isto correu». O mais antigo responderia a
		// outra pergunta.
		var ultimo time.Time
		for _, cp := range a.Checkpoints {
			if cp.Timestamp.After(ultimo) {
				ultimo = cp.Timestamp
			}
		}
		if !ultimo.IsZero() {
			g("aos_worm_anchor_age_seconds", "Segundos desde a ULTIMA selagem que produziu esta ancora. Com cadencia diaria, acima de 172800 (48h) a tarefa de selagem morreu — o dobro da cadencia, para que um dia falhado nao alerte e dois sim.",
				"gauge", time.Since(ultimo).Seconds(), "")
		}
	}

	// EXPORTAÇÃO OTLP (AOS-173/DEF-012) — a única via por onde a observabilidade pode morrer sem
	// parar mais nada.
	//
	// O exporter é FAIL-OPEN por decisão declarada: a recusa do colector não quebra o run. É a
	// escolha certa — observabilidade não deve derrubar o caminho de negócio — mas tem um preço,
	// e o preço é este: uma má configuração (certificado de cliente errado, colector que rejeita)
	// pára os spans sem parar nada mais.
	//
	// O inventário dizia que isso acontecia «em silêncio». Não acontece: cada lote falhado ESCREVE
	// no log. O que faltava era outra coisa — nenhum destes números saía em `/metrics`, pelo que a
	// falha não era ALERTÁVEL: só se via lendo logs, à mão, depois de alguém desconfiar.
	//
	// `failed` cresce quando o colector recusa ou está em baixo; `dropped` quando a fila enche
	// (o produtor não bloqueia — é o que mantém o fail-open). Os dois a subir com `exported`
	// parado é a assinatura exacta de uma autenticação mal configurada.
	if h.node != nil && h.node.otlp != nil {
		s := h.node.otlp.Stats()
		g("aos_otlp_spans_exported_total", "Spans que um POST 2xx do colector confirmou.", "counter", float64(s.Exported), "")
		g("aos_otlp_spans_failed_total", "Spans cujo export FALHOU apos as retentativas (fail-open: o run seguiu).", "counter", float64(s.Failed), "")
		g("aos_otlp_spans_dropped_total", "Spans descartados por fila cheia (o produtor nunca bloqueia).", "counter", float64(s.Dropped), "")
		g("aos_otlp_batches_total", "Lotes de export tentados.", "counter", float64(s.Batches), "")
	}

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
	// A1/A3: a correcção em si NÃO entra no selo — é conteúdo, e o trilho é sem PII. O que entra
	// é QUEM interveio, SOBRE QUE run e COM QUE tipo de sinal. Ver control_seal.go.
	h.sealControlAction(r.Context(), "steer", runID, emitter.ID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "steered"})
}

// handlePause emite um pause gracioso AUTENTICADO (mesma fronteira do steer).
func (h *apiHandler) handlePause(w http.ResponseWriter, r *http.Request) {
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
	h.sealControlAction(r.Context(), "pause", runID, emitter.ID)
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

// challengeRequest pede a EMISSÃO de um challenge fresco para uma cerimónia de aprovação
// (AOS-266). O scope é derivado de request_id (o mesmo âmbito anti-replay que o /approve usa),
// e o challenge é atribuído ao aprovador nomeado — só ele o poderá usar numa perna válida.
type challengeRequest struct {
	RequestID string `json:"request_id"`
	Approver  string `json:"approver"`
}

// handleChallenge EMITE um challenge server-side para (request_id, approver) e regista-o
// duravelmente com TTL (AOS-266, achado F10). É o LADO DE EMISSÃO da frescura por-cerimónia: o
// operador/aprovador pede aqui o challenge, assina a perna com ele (aos-issuer approve-sign
// --challenge <hex>) e submete em /approve; o gate, composto com integration.WithChallengeIssuance,
// só aceita challenges que constem deste registo e estejam frescos.
//
// DESLIGADO (501) quando a frescura está DORMENTE (Node.ChallengeIssuer nil) — o mesmo valor que
// alimenta a porta do gate. Emissão e composição são INDIVISÍVEIS por construção (ver Bootstrap):
// nunca há a porta ligada sem este endpoint operável, nem o contrário.
//
// O challenge NÃO é segredo (viaja em claro no clientDataJSON da attestation); é devolvido em
// base64 (para o wire do /approve) e em hex (para a flag --challenge do aos-issuer), sem obrigar
// a reescrever o cliente. Autenticado pela MESMA admission do plano de controlo que o /approve.
func (h *apiHandler) handleChallenge(w http.ResponseWriter, r *http.Request) {
	if h.node.ChallengeIssuer == nil {
		writeError(w, http.StatusNotImplemented, "emissao de challenges desligada (frescura por-cerimonia dormente; defina AOS_CHALLENGE_ISSUANCE=1)")
		return
	}
	var req challengeRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	approver := strings.TrimSpace(req.Approver)
	if requestID == "" || approver == "" {
		writeError(w, http.StatusBadRequest, "request_id e approver obrigatorios")
		return
	}
	// O scope EXPORTADO amarra a emissão ao MESMO namespace que a verificação (checkIssued)
	// consulta — uma divergência de prefixo faria o gate negar toda a perna.
	challenge, err := h.node.ChallengeIssuer.IssueChallenge(r.Context(), integration.ChallengeScope(requestID), approver)
	if err != nil {
		// Falha do backend durável do Event Store ⇒ 500 (sem detalhe no corpo).
		writeError(w, http.StatusInternalServerError, "emissao de challenge falhou")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge":     base64.StdEncoding.EncodeToString(challenge), // wire do /approve
		"challenge_hex": hex.EncodeToString(challenge),                // flag --challenge do aos-issuer
	})
}

// handleApprove autoriza uma acção irreversível via o FourEyesGate (AOS-162), SE o nó o
// compôs; senão o endpoint está DESLIGADO (501). Non-signing: as assinaturas das pernas
// vêm no corpo; o gate verifica-as contra as pubkeys pinadas. Negação ⇒ 403 SEM efeito.
func (h *apiHandler) handleApprove(w http.ResponseWriter, r *http.Request) {
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
	// QUANTOS HUMANOS SÃO PRECISOS É DECISÃO DO NÓ, e não de quem pede. Ver
	// [exigenciaDeDuploControlo] para o achado 1.10 e para a razão de a fonte ser o registo
	// pendente que o Reference Monitor selou ANTES de haver pedido nenhum.
	exigirDuplo, pendente, reconhecida, expirado, derr := exigenciaDeDuploControlo(
		r.Context(), h.node.PendingApprovals, r.PathValue("id"), preview,
		req.Request.DualControlRequired, h.svc.approvalTTL)
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "nao foi possivel classificar a accao")
		return
	}
	if expirado {
		// PENDENTE JÁ FORA DO PRAZO. Sem esta recusa, a expiração era uma corrida com o varredor:
		// entre cruzar o TTL e o tick seguinte (1 min por omissão, INFINITO com o varredor
		// desligado) a cerimónia produzia um grant válido, com TTL fresco — convertendo o
		// «vai ser negado» que o TTL decide num «autorizado».
		h.svc.log("four-eyes (completude 2026-08-23): cerimonia RECUSADA — o pendente do run %q passo %q ja passou o prazo de %s e a accao vai ser NEGADA; o varredor ainda nao lhe chegou, mas o prazo nao e uma corrida com ele",
			pendente.RunID, pendente.StepID, h.svc.approvalTTL)
		writeError(w, http.StatusForbidden, "o pedido de aprovacao expirou")
		return
	}
	if h.node.PendingApprovals != nil && !reconhecida {
		// PREVIEW QUE O NÓ NUNCA ESCALOU. Sem isto, quem pede escolhe a preview E o número de
		// aprovadores: inventa um efeito, assina-o com um humano, e recebe um grant que a
		// mediação depois honra — porque a amarra compara o grant com a MESMA preview inventada.
		//
		// O registo de pendentes é composto sempre que o four-eyes o é (mesmo bloco do
		// bootstrap), pelo que esta recusa não desliga aprovações em nó nenhum que as tenha.
		h.svc.log("four-eyes (achado 1.10): cerimonia RECUSADA — a preview nao corresponde a nenhuma escalada pendente do run %q; o no so aprova efeitos que ele proprio escalou", r.PathValue("id"))
		writeError(w, http.StatusForbidden, "preview nao corresponde a nenhuma escalada pendente")
		return
	}
	if exigirDuplo && !req.Request.DualControlRequired {
		// O corpo declarou que UMA perna basta para uma acção que o nó classifica como
		// IRREVERSÍVEL. Recusa-se — e não se corrige em silêncio, porque o
		// `dual_control_required` entra na MENSAGEM ASSINADA de cada perna: sobrepô-lo seria
		// verificar assinaturas contra uma afirmação diferente daquela que os humanos assinaram.
		//
		// A cerimónia repete-se com a exigência certa declarada, e aí as assinaturas cobrem a
		// afirmação verdadeira.
		h.svc.log("four-eyes (achado 1.10): cerimonia RECUSADA — o pedido declarou dual_control_required=false e a capability %q e IRREVERSIVEL; repita a cerimonia declarando duplo controlo, para que as pernas assinem a exigencia REAL", pendente.Capability)
		writeError(w, http.StatusForbidden, "esta accao exige duplo controlo — repita a cerimonia com dual_control_required=true")
		return
	}
	// A CLASSE DE RISCO É DO NÓ, pela mesma razão e com a mesma forma. Ver [classeExigida]: do
	// pendente deriva-se um LIMITE INFERIOR — irreversível ⇒ danger — e nada mais.
	//
	// RECUSA-SE, não se corrige, e a razão é a mesma do duplo controlo: a classe entra na
	// MENSAGEM ASSINADA de cada perna. Sobrepô-la em silêncio seria verificar assinaturas contra
	// uma afirmação diferente da que os humanos assinaram — e nem verificariam, porque a
	// assinatura deixaria de casar.
	if exigida, derivavel := classeExigida(pendente); reconhecida && derivavel &&
		risk.Class(req.Request.RiskClass) != exigida {
		h.svc.log("four-eyes (completude 2026-08-23): cerimonia RECUSADA — o pedido declarou risk_class=%q e a capability %q e IRREVERSIVEL, logo exige %q. Sem esta recusa, aprovadores com autoridade `approve:%s` destravariam um efeito irreversivel: a classe declarada escolhe o escalao de autoridade exigido a cada perna",
			risk.Class(req.Request.RiskClass).String(), pendente.Capability, exigida.String(),
			risk.Class(req.Request.RiskClass).String())
		writeError(w, http.StatusForbidden, "esta accao e irreversivel — repita a cerimonia com risk_class=danger")
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
			// ID DE APROVAÇÃO REUTILIZADO (achado 1.11) — nomeado no LOG DO OPERADOR, embora a
			// resposta continue uniforme. O 403 uniforme existe para não revelar ao chamador qual
			// invariante falhou; este caso, porém, é o único que significa «dois humanos
			// completaram uma cerimónia que NÃO ficou registada», e ninguém o investigaria a
			// partir de um 403 igual a todos os outros.
			if errors.Is(gerr, integration.ErrGrantIDReused) {
				h.svc.log("four-eyes (achado 1.11): cerimonia RECUSADA — o request_id %q ja tem na cadeia um grant com conteudo DIFERENTE. Sem esta recusa, os aprovadores desta cerimonia receberiam 200 e a prova registada nomearia OUTRO par: %v", feReq.RequestID, gerr)
			}
			// Fail-closed e resposta UNIFORME: não se revela qual invariante falhou (o audit
			// tem o erro dedicado).
			writeError(w, http.StatusForbidden, "aprovacao recusada")
			return
		}
		// A3: a cadeia selava que UM gate humano fora satisfeito, sem selar QUEM. Para uma
		// autorização cujo propósito é o não-repúdio, era a peça em falta. O selo nomeia os
		// aprovadores e amarra o grant que destrava a acção na retoma.
		obl := approversObligation(grant.Approvers)
		if grant.ID != "" {
			obl = append(obl, audit.Obligation{Type: controlGrantObl, Fields: []string{grant.ID}})
		}
		h.sealControlAction(r.Context(), "approve", feReq.RequestID, strings.Join(grant.Approvers, ","), obl...)
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
	h.sealControlAction(r.Context(), "approve", feReq.RequestID,
		strings.Join(decision.Approvers, ","), approversObligation(decision.Approvers)...)
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
	case errors.Is(err, ErrExhaustionPromptUnanswered):
		// 409, e NOMEANDO a via: o run existe, está suspenso e a retoma é legítima — só ainda
		// não é a vez dela. Um 404 uniforme aqui esconderia do operador a única acção que
		// destranca o run, e um 403 sugeriria falta de autoridade quando o que falta é a
		// DECISÃO. Não revela nada que o GET do mesmo run não mostre.
		writeError(w, http.StatusConflict, "ha um prompt de exaustao de orcamento POR RESPONDER neste run (pending_exhaustion em GET /runs/{id}) — decida em "+exhaustionDecisionRoute+" (\""+exhaustionOptionContinue+"\" ou \""+exhaustionOptionAbort+"\", assinado por operador registado) antes de retomar; sem decisao, o TTL de pendentes expira a pergunta e a retoma volta a ser aceite")
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

// Close FECHA o http.Server à força, sem esperar por ligações activas. É o par documentado
// do [APIServer.Shutdown]: quando o dreno gracioso não cabe no seu orçamento, as ligações
// remanescentes ficam — e um SSE de trajectória ocioso limpa o write-deadline entre escritas
// de propósito, logo nunca fecha sozinho. Ver [encerrarGraciosamente].
func (s *APIServer) Close() error { return s.http.Close() }
