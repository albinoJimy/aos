package main

// AOS-277 — OS TRÊS NÚMEROS DA ADMISSION DE INGRESSO, AFINÁVEIS PELO OPERADOR.
//
// CORRECÇÃO DE FACTO que este ticket assenta: o ingresso do plano de DADOS **já tinha**
// backpressure desde AOS-166 — `handleSubmit` passa por um token-bucket e por um tecto de
// runs em curso, e responde 429 quando qualquer um deles fecha (api.go, na entrada de
// `POST /runs`). O que NÃO existia era a superfície para o operador os afinar: os três
// números viviam como constantes do binário ([DefaultRatePerSec], [DefaultRateBurst],
// [DefaultMaxInFlight]) e as [APIOption] que os mudam ([WithRateLimit], [WithMaxInFlight])
// só eram alcançáveis por testes. Este ficheiro é ESSA superfície, e NADA MAIS: não
// constrói limitador nenhum, não muda a semântica da admission e não toca no caminho de
// pedido.
//
//   - AOS_INGRESS_RATE — reabastecimento do balde, em tokens (= pedidos) por SEGUNDO;
//   - AOS_INGRESS_BURST — capacidade do balde: quantos pedidos são absorvidos de uma vez
//     com o balde cheio;
//   - AOS_INGRESS_MAX_INFLIGHT — tecto de runs EM CURSO nesta réplica;
//   - AOS_INGRESS_MAX_INFLIGHT_PER_CALLER — tecto de runs EM CURSO POR SUBMISSOR (AOS-456).
//     Vazia ⇒ tecto por-chamador NÃO COMPOSTO, e o banner declara-o.
//
// FAIL-CLOSED NA CONFIGURAÇÃO (molde de [ErrBadBreakerThresholds]/[ErrBadRetention]/
// [ErrBadBudget]): qualquer das três com valor ilegível, não-finito, negativo ou ZERO
// **ABORTA** o arranque. Nenhuma degrada em silêncio para o default — um operador que se
// engana a escrever um limite de admissão NÃO deve ficar com um nó que admite um número
// diferente do que ele julga.
//
// PORQUE É QUE ZERO É INVÁLIDO, nas três (e não "desligado"):
//
//   - `AOS_INGRESS_RATE=0` seria um balde que NUNCA reabastece: passados os primeiros
//     `burst` pedidos, TODO o `POST /runs` desta réplica ficaria em 429 para sempre. É um
//     modo legítimo em teste determinístico ([WithRateLimit] aceita-o de propósito), mas
//     por variável de ambiente é indistinguível de "sem limite" — e o engano custa o
//     ingresso inteiro.
//   - `AOS_INGRESS_BURST` abaixo de 1 é pior ainda: o balde nunca acumula o token inteiro
//     que `allow` consome, logo NENHUM pedido é admitido, nem o primeiro.
//   - `AOS_INGRESS_MAX_INFLIGHT=0` DESLIGA o tecto (a guarda é `> 0`) — isto é, o valor
//     que mais se parece com "nenhum run permitido" faz exactamente o oposto. Uma
//     armadilha destas não entra na superfície do operador.
//
// Quem quer os limites por omissão deixa a variável POR DEFINIR. Não há valor que DESLIGUE
// um limite: desligar backpressure não é uma afinação, é uma decisão de âmbito diferente
// (e ficaria por fazer noutro ticket, com o banner a declará-la).

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// ErrBadIngressLimits — um dos três knobs de ingresso está definido mas é inválido. O nó
// recusa arrancar em vez de silenciosamente aplicar o default (o operador ficaria
// convencido de que o nó admite o que ele escreveu) ou de aplicar um valor degenerado que
// fecha o ingresso por inteiro.
var ErrBadIngressLimits = errors.New("aos: limites de ingresso mal configurados — AOS_INGRESS_RATE (pedidos/segundo, numero finito > 0), AOS_INGRESS_BURST (capacidade do balde, numero finito >= 1) AOS_INGRESS_MAX_INFLIGHT (inteiro > 0) e AOS_INGRESS_MAX_INFLIGHT_PER_CALLER (inteiro > 0 e NAO superior a AOS_INGRESS_MAX_INFLIGHT; vazia => tecto por-chamador NAO COMPOSTO). Deixe a variavel POR DEFINIR para manter o default; NENHUM valor desliga o limite (0 nao desliga: no rate/burst fecharia o ingresso, no max-inflight abriria-o sem tecto)")

// ingressLimits são os três números EFECTIVAMENTE em vigor na admission de `POST /runs`
// — os defaults do binário quando as variáveis não estão definidas, o valor lido quando
// estão. É este valor (nunca a intenção da config) que alimenta as [APIOption] E o banner,
// para que a linha anunciada e a postura ligada sejam a MESMA coisa por construção.
type ingressLimits struct {
	ratePerSec  float64
	burst       float64
	maxInFlight int
	// inFlightPerCaller é o tecto de runs EM CURSO por SUBMISSOR (AOS-456). 0 ⇒ NÃO COMPOSTO, e o
	// banner declara-o.
	inFlightPerCaller int
	// tuned diz se ALGUMA das variáveis foi definida. Vive AQUI (e não num parâmetro
	// do banner) para que o texto do banner não possa divergir do que a leitura viu: a
	// origem dos números e os números são o MESMO valor de retorno.
	tuned bool
}

// ingressLimitsFromEnv lê as três variáveis UMA vez (é chamada uma só vez, no arranque,
// por [serveAPI]) e devolve os limites resolvidos mais as [APIOption] que os aplicam.
//
// As variáveis são lidas com nomes LITERAIS aqui de propósito: o gate AOS-203
// (TestAOS203EnvSurfaceIsDocumented) extrai a superfície de ambiente por AST e uma leitura
// com nome não-literal falha o teste — a validação vive em helpers que recebem o valor
// RAW, não o nome.
func ingressLimitsFromEnv() (ingressLimits, []APIOption, error) {
	lim := ingressLimits{
		ratePerSec:  DefaultRatePerSec,
		burst:       DefaultRateBurst,
		maxInFlight: DefaultMaxInFlight,
	}

	rawRate := strings.TrimSpace(os.Getenv("AOS_INGRESS_RATE"))
	if rawRate != "" {
		v, ok := parsePositiveFloat(rawRate, 0)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_RATE=%q", ErrBadIngressLimits, rawRate)
		}
		lim.ratePerSec, lim.tuned = v, true
	}

	rawBurst := strings.TrimSpace(os.Getenv("AOS_INGRESS_BURST"))
	if rawBurst != "" {
		// Mínimo 1: abaixo disso o balde nunca acumula o token inteiro que `allow` consome.
		v, ok := parsePositiveFloat(rawBurst, 1)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_BURST=%q", ErrBadIngressLimits, rawBurst)
		}
		lim.burst, lim.tuned = v, true
	}

	rawInFlight := strings.TrimSpace(os.Getenv("AOS_INGRESS_MAX_INFLIGHT"))
	if rawInFlight != "" {
		n, err := strconv.Atoi(rawInFlight)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_MAX_INFLIGHT=%q", ErrBadIngressLimits, rawInFlight)
		}
		lim.maxInFlight, lim.tuned = n, true
	}

	// AOS-456 — tecto de concorrência POR-CHAMADOR.
	//
	// VALIDADO NOS DOIS SENTIDOS, e o superior não é zelo: a tentativa 1 deste ticket validou só
	// `> 0` num tecto de tabela, e um `100000000` era aceite sem uma palavra — fail-closed contra
	// o zero, aberto de par em par contra o absurdo. Aqui o tecto superior tem significado
	// próprio: um tecto por-chamador ACIMA do global nunca morde (o global morde primeiro), logo
	// é configuração que anuncia uma barreira inerte. Recusa-se em vez de a deixar mentir.
	rawPerCaller := strings.TrimSpace(os.Getenv("AOS_INGRESS_MAX_INFLIGHT_PER_CALLER"))
	if rawPerCaller != "" {
		n, err := strconv.Atoi(rawPerCaller)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_MAX_INFLIGHT_PER_CALLER=%q", ErrBadIngressLimits, rawPerCaller)
		}
		if n > lim.maxInFlight {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_MAX_INFLIGHT_PER_CALLER=%q excede AOS_INGRESS_MAX_INFLIGHT=%d — um tecto por-chamador acima do global NUNCA morde (o global morde primeiro) e anunciaria uma barreira inerte", ErrBadIngressLimits, rawPerCaller, lim.maxInFlight)
		}
		lim.inFlightPerCaller, lim.tuned = n, true
	}

	return lim, []APIOption{
		WithRateLimit(lim.ratePerSec, lim.burst),
		WithMaxInFlight(lim.maxInFlight),
	}, nil
}

// parsePositiveFloat valida um número FINITO >= min e > 0. Recebe o valor RAW (não o nome
// da variável) para que a leitura do ambiente fique sempre literal no chamador — ver a
// nota do gate AOS-203 em [ingressLimitsFromEnv]. A rejeição explícita de NaN/±Inf não é
// zelo: `strconv.ParseFloat` aceita "Inf" e "NaN", e um `+Inf` passaria um teste ingénuo
// de `> 0` para dentro do balde.
func parsePositiveFloat(raw string, min float64) (float64, bool) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v < min {
		return 0, false
	}
	return v, true
}

// ingressPostureBanner declara os limites de ingresso EM VIGOR, a partir dos números
// REALMENTE aplicados às [APIOption] — nunca da intenção da config (a mesma disciplina de
// [budgetPostureBanner]/[modelPostureBanner]: postura anunciada = postura ligada).
//
// A linha declara também o ALCANCE, porque é aí que uma leitura optimista se enganaria e
// AOS-248 proíbe promessas a mais. Cada afirmação está amarrada ao código:
//
//   - COBRE `POST /runs` e SÓ. O bucket do plano de dados (`apiHandler.bucket`) tem UM
//     consumidor — a admission de `handleSubmit` — e o tecto de in-flight é lido no MESMO
//     sítio. Nenhum outro handler os consulta.
//   - NÃO cobre o plano de CONTROLO. `/steer`,`/pause`,`/approve`,`/resume` passam por
//     `admitControl`, que usa um bucket DEDICADO (`ctrlBucket`) alimentado por
//     [WithControlRateLimit] — que estas variáveis NÃO tocam (continua nos defaults).
//   - NÃO cobre as LEITURAS. `GET /runs/{id}` não é limitado por taxa nenhuma; o stream
//     SSE de trajectória tem o seu próprio tecto de ligações ([DefaultMaxTrajectoryConns]),
//     também fora destas variáveis.
//   - É POR-PROCESSO e GLOBAL entre chamadores: o balde vive em memória nesta réplica e
//     não é por-IP nem por-principal. N réplicas ⇒ N vezes o limite; e um único cliente
//     ruidoso pode esgotar o balde para todos (a equidade entre chamadores não existe
//     neste mecanismo).
//   - O tecto de in-flight mede RUNS REGISTADOS no loop de serviço
//     (`NodeService.InProgressCount`). Um run SUSPENSO à espera de aval humano SAI desse
//     balde (passa a `suspended`), logo NÃO conta para o tecto: o número trava execução
//     concorrente, não ocupação de trabalho pendente.
//   - A RETOMA (`POST /runs/{id}/resume`) re-hospeda um run SEM consultar o tecto: só
//     `handleSubmit` o lê. O tecto trava admissões NOVAS, não o total de runs vivos.
//   - O 429 é seco: `writeError` não emite `Retry-After`, pelo que o cliente não recebe
//     indicação de quando repetir.
//
// ingressPostureBanner declara os limites EM VIGOR. Os dois booleanos são COMPOSIÇÕES REAIS, não
// config — a tentativa 1 deste ticket derivava a postura só de `lim` e anunciava «LIGADA» com a
// barreira a `nil`. E são DOIS porque uma revisão adversarial mediu que colapsá-los num era o mesmo
// defeito noutra forma:
//
//   - `gateComposto` ([noTemGateSoberanoDeLeitura]): o tecto por-chamador está em vigor;
//   - `principalVerificavel` ([principalDoRunEVerificavel]): e a atribuição é INFORJÁVEL.
//
// Com o gate composto e SEM credencial forte o principal vem do header `X-Aos-Reader`, que o
// chamador escreve — medido: 60 submissões com o header a rodar, 60 admitidas, tecto a 2. O tecto
// compõe-se nessa postura (vale contra rajada honesta) mas o banner tem de dizer QUAL das duas é.
func ingressPostureBanner(lim ingressLimits, gateComposto, principalVerificavel bool) []string {
	origem := "nos DEFAULTS do binario (nenhuma de AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT/AOS_INGRESS_MAX_INFLIGHT_PER_CALLER definida)"
	if lim.tuned {
		origem = "AFINADO por AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT/AOS_INGRESS_MAX_INFLIGHT_PER_CALLER"
	}
	// DOBRA DO TECTO POR-CHAMADOR (AOS-456) — três posturas DISTINGUÍVEIS, e a do meio é a que
	// a revisão adversarial da tentativa 1 apanhou: configurado e inerte.
	porChamador := " TECTO POR-CHAMADOR (AOS-456): NAO CONFIGURADO — AOS_INGRESS_MAX_INFLIGHT_PER_CALLER vazia, logo o tecto de runs em curso e SO global e uma rajada de um chamador pode ocupar todos os lugares."
	// O ALCANCE EXACTO, e o que ele NAO da — as tres frases sao achados de revisao adversarial e
	// nenhuma e opcional:
	//
	//  (1) «admite N submissoes NOVAS simultaneas», nao «ocupa N lugares»: as duas isencoes
	//      (suspenso sai da contagem, retoma nao consulta o tecto) COMPOEM-SE, e medidos deram 20
	//      runs em `s.runs` de um submissor com o tecto a 1;
	//  (2) o 429 por-chamador GASTA um token do balde global, porque o balde e consumido no topo do
	//      handler e a decisao por-chamador acontece no submit. A rajada de A nao tira LUGARES a B,
	//      mas gasta TAXA comum — a justica em taxa e o eixo 456b, e nao esta feita;
	//  (3) o tecto tem um PISO pratico ditado por quem submete em paralelo: o `aos-orq` despacha ate
	//      16 runs-filho por plano, todos imputados ao mesmo submissor.
	alcance := " ALCANCE do tecto por-chamador: admite N submissoes NOVAS simultaneas — NAO e um tecto de OCUPACAO: um run SUSPENSO a espera de aval humano SAI da contagem e a RETOMA (/resume) NAO a consulta, pelo que um submissor pode ter MAIS de N runs em `s.runs` (medido: 20 com o tecto a 1). E o 429 por-chamador GASTA um token do balde GLOBAL (o balde e consumido no topo do handler, a decisao por-chamador no submit): a rajada de um chamador nao tira LUGARES aos outros, mas gasta TAXA comum — justica em TAXA e o eixo AOS-456b, NAO esta feita. PISO PRATICO: o aos-orq despacha ate 16 runs-filho por plano, todos do mesmo submissor, pelo que um valor abaixo de 16 parte planos com fan-out."
	switch {
	case lim.inFlightPerCaller > 0 && principalVerificavel:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): LIGADO sobre principal VERIFICADO — cada submissor admite no maximo %d submissao(oes) NOVA(s) simultanea(s); exceder responde 429 sem ocupar lugar nenhum. A atribuicao vem de credencial FORTE verificada (OIDC), logo nao e forjavel pelo chamador.%s",
			lim.inFlightPerCaller, alcance)
	case lim.inFlightPerCaller > 0 && gateComposto:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): LIGADO sobre principal DEMO-GRADE (%d) — ATENCAO: sem credencial forte composta o principal vem do header X-Aos-Reader, que o CHAMADOR escreve, pelo que este tecto CONTORNA-SE rodando o header (medido: 60 submissoes rotativas, 60 admitidas com o tecto a 2). Vale contra rajada HONESTA ou cliente mal configurado; NAO vale contra abuso. Para o tornar inforjavel defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE (AOS_MODE=production ja os exige).%s",
			lim.inFlightPerCaller, alcance)
	case lim.inFlightPerCaller > 0:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): CONFIGURADO (%d) mas NAO COMPOSTO — sem gate soberano de leitura o principal do run vem do CORPO do pedido (auto-declarado), e um tecto sobre um valor que o chamador escolhe contorna-se mudando-o. NAO esta em vigor: defina AOS_BOARD_REGIONS (e o WORM).",
			lim.inFlightPerCaller)
	}
	return []string{
		fmt.Sprintf("ingresso / admission (AOS-166/AOS-277): LIGADO e %s — POST /runs admite %.4g pedido(s)/segundo com burst de %.4g e no maximo %d run(s) EM CURSO nesta replica; exceder qualquer um responde 429. ALCANCE: cobre POST /runs e SO — o plano de CONTROLO (/steer,/pause,/approve,/resume) tem um balde DEDICADO que estas variaveis NAO afinam, as leituras (GET /runs/{id}) nao tem limite de taxa nenhum e o stream SSE de trajectoria tem o seu proprio tecto de ligacoes, tambem fora destas variaveis. O balde e POR-PROCESSO, em memoria e GLOBAL entre chamadores: NAO e por-IP nem por-principal (um so cliente ruidoso pode esgota-lo para todos) e N replicas valem N vezes este limite — nao ha limite de admissao agregado no cluster. O tecto de in-flight conta os runs REGISTADOS no loop de servico: um run SUSPENSO a espera de aval humano SAI dessa contagem e NAO ocupa lugar, e a RETOMA (/resume) re-hospeda SEM consultar o tecto. O 429 nao leva Retry-After.%s",
			origem, lim.ratePerSec, lim.burst, lim.maxInFlight, porChamador),
	}
}
