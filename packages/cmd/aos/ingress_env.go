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
//   - AOS_INGRESS_MAX_INFLIGHT — tecto de runs EM CURSO nesta réplica.
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
var ErrBadIngressLimits = errors.New("aos: limites de ingresso mal configurados — AOS_INGRESS_RATE (pedidos/segundo, numero finito > 0), AOS_INGRESS_BURST (capacidade do balde, numero finito >= 1) AOS_INGRESS_MAX_INFLIGHT (inteiro > 0), AOS_INGRESS_PER_CALLER_MAX (inteiro > 0, tecto da tabela de baldes por-principal), AOS_INGRESS_PER_CALLER_RATE e AOS_INGRESS_PER_CALLER_BURST (a forma de cada balde por-chamador; por omissao iguais aos globais). Deixe a variavel POR DEFINIR para manter o default; NENHUM valor desliga o limite (0 nao desliga: no rate/burst fecharia o ingresso, no max-inflight abriria-o sem tecto)")

// ingressLimits são os três números EFECTIVAMENTE em vigor na admission de `POST /runs`
// — os defaults do binário quando as variáveis não estão definidas, o valor lido quando
// estão. É este valor (nunca a intenção da config) que alimenta as [APIOption] E o banner,
// para que a linha anunciada e a postura ligada sejam a MESMA coisa por construção.
type ingressLimits struct {
	ratePerSec  float64
	burst       float64
	maxInFlight int
	// perCaller* são a 2.ª etapa (AOS-456): tecto da TABELA de baldes por-principal, e a forma
	// de cada balde. `perCallerMax == 0` ⇒ etapa NÃO COMPOSTA, e o banner declara-o.
	perCallerMax   int
	perCallerRate  float64
	perCallerBurst float64
	// tuned diz se ALGUMA das três variáveis foi definida. Vive AQUI (e não num parâmetro
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
	lim.perCallerMax = DefaultPerCallerMax

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

	rawPerMax := strings.TrimSpace(os.Getenv("AOS_INGRESS_PER_CALLER_MAX"))
	if rawPerMax != "" {
		n, err := strconv.Atoi(rawPerMax)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_PER_CALLER_MAX=%q", ErrBadIngressLimits, rawPerMax)
		}
		lim.perCallerMax, lim.tuned = n, true
	}

	rawPerRate := strings.TrimSpace(os.Getenv("AOS_INGRESS_PER_CALLER_RATE"))
	if rawPerRate != "" {
		v, ok := parsePositiveFloat(rawPerRate, 0)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_PER_CALLER_RATE=%q", ErrBadIngressLimits, rawPerRate)
		}
		lim.perCallerRate, lim.tuned = v, true
	}

	rawPerBurst := strings.TrimSpace(os.Getenv("AOS_INGRESS_PER_CALLER_BURST"))
	if rawPerBurst != "" {
		v, ok := parsePositiveFloat(rawPerBurst, 1)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_PER_CALLER_BURST=%q", ErrBadIngressLimits, rawPerBurst)
		}
		lim.perCallerBurst, lim.tuned = v, true
	}

	// Por omissão cada balde tem a MESMA forma que o global. Ver o banner: com esta forma a 2.ª
	// etapa dá ATRIBUIÇÃO e um tecto por-chamador, mas NÃO protege B da rajada de A — para isso
	// a dotação por-chamador tem de ser estritamente MENOR que a global.
	perRate, perBurst := lim.perCallerRate, lim.perCallerBurst
	if perRate <= 0 {
		perRate = lim.ratePerSec
	}
	if perBurst <= 0 {
		perBurst = lim.burst
	}
	lim.perCallerRate, lim.perCallerBurst = perRate, perBurst

	return lim, []APIOption{
		WithRateLimit(lim.ratePerSec, lim.burst),
		WithMaxInFlight(lim.maxInFlight),
		WithPerCallerRateLimit(lim.perCallerMax, lim.perCallerRate, lim.perCallerBurst),
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
func ingressPostureBanner(lim ingressLimits) []string {
	// DOBRA DA 2.ª ETAPA (AOS-456). O texto DERIVA dos números em vigor e da comparação entre
	// eles — não de uma frase fixa. É a disciplina do resto deste ficheiro: postura anunciada =
	// postura ligada. E diz a VERDADE INCÓMODA quando a dotação por-chamador iguala a global:
	// nesse caso a etapa dá atribuição mas NÃO protege um chamador da rajada de outro, porque o
	// balde global esgota-se primeiro.
	porChamador := " 2.a ETAPA (por-chamador, AOS-456): NAO COMPOSTA — sem gate soberano nao ha principal verificado a que imputar consumo, logo o unico tecto e o global acima."
	if lim.perCallerMax > 0 {
		justo := "NAO protege um chamador da rajada de outro: a dotacao por-chamador IGUALA a global, logo o balde global esgota-se primeiro e o 429 atinge todos. Para justica entre pares, ponha AOS_INGRESS_PER_CALLER_RATE/_BURST ESTRITAMENTE ABAIXO de AOS_INGRESS_RATE/_BURST"
		if lim.perCallerRate < lim.ratePerSec || lim.perCallerBurst < lim.burst {
			justo = "PROTEGE um chamador da rajada de outro: a dotacao por-chamador e menor que a global, logo quem inunda esgota o SEU balde antes de drenar o comum"
		}
		porChamador = fmt.Sprintf(" 2.a ETAPA (por-chamador, AOS-456): LIGADA — cada principal verificado tem o seu balde de %.4g pedido(s)/segundo com burst de %.4g, numa tabela de no maximo %d principais; cheia a tabela, evicta-se o balde MAIS CHEIO (que e o que menos estado perde, e nunca o do atacante, cujo balde e o mais vazio). %s. So o POST /runs passa por ela; o plano de controlo e as leituras NAO.",
			lim.perCallerRate, lim.perCallerBurst, lim.perCallerMax, justo)
	}
	origem := "nos DEFAULTS do binario (nenhuma de AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT definida)"
	if lim.tuned {
		origem = "AFINADO por AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT"
	}
	return []string{
		fmt.Sprintf("ingresso / admission (AOS-166/AOS-277): LIGADO e %s — POST /runs admite %.4g pedido(s)/segundo com burst de %.4g e no maximo %d run(s) EM CURSO nesta replica; exceder qualquer um responde 429. ALCANCE: cobre POST /runs e SO — o plano de CONTROLO (/steer,/pause,/approve,/resume) tem um balde DEDICADO que estas variaveis NAO afinam, as leituras (GET /runs/{id}) nao tem limite de taxa nenhum e o stream SSE de trajectoria tem o seu proprio tecto de ligacoes, tambem fora destas variaveis. O balde da 1.a etapa e POR-PROCESSO, em memoria e GLOBAL entre chamadores (nao e por-IP), e N replicas valem N vezes este limite — nao ha limite de admissao agregado no cluster.%s O tecto de in-flight conta os runs REGISTADOS no loop de servico: um run SUSPENSO a espera de aval humano SAI dessa contagem e NAO ocupa lugar, e a RETOMA (/resume) re-hospeda SEM consultar o tecto. O 429 nao leva Retry-After",
			origem, lim.ratePerSec, lim.burst, lim.maxInFlight, porChamador),
	}
}
