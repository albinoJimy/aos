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
//   - AOS_INGRESS_MAX_INFLIGHT_PER_CALLER — tecto de SUBMISSÕES NOVAS simultâneas POR
//     SUBMISSOR (AOS-456). NÃO é um tecto de ocupação — ver a nota do banner.
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
var ErrBadIngressLimits = errors.New("aos: limites de ingresso mal configurados — AOS_INGRESS_RATE (pedidos/segundo, numero finito > 0), AOS_INGRESS_BURST (capacidade do balde, numero finito >= 1) AOS_INGRESS_MAX_INFLIGHT (inteiro > 0) e AOS_INGRESS_MAX_INFLIGHT_PER_CALLER (inteiro > 0 e ESTRITAMENTE MENOR que AOS_INGRESS_MAX_INFLIGHT; vazia => tecto por-chamador NAO COMPOSTO). Deixe a variavel POR DEFINIR para manter o default; NENHUM valor desliga o limite (0 nao desliga: no rate/burst fecharia o ingresso, no max-inflight abriria-o sem tecto)")

// ingressLimits são os três números EFECTIVAMENTE em vigor na admission de `POST /runs`
// — os defaults do binário quando as variáveis não estão definidas, o valor lido quando
// estão. É este valor (nunca a intenção da config) que alimenta as [APIOption] E o banner,
// para que a linha anunciada e a postura ligada sejam a MESMA coisa por construção.
type ingressLimits struct {
	ratePerSec  float64
	burst       float64
	maxInFlight int
	// readRatePerSec/readBurst são a admission de TAXA do plano de DADOS inteiro (AOS-458) — o
	// balde que o invólucro da rota consome ANTES de qualquer verificação criptográfica.
	readRatePerSec float64
	readBurst      float64
	// trajMaxConns / trajMaxConnsPerReader são o tecto de streams SSE de trajectória e a sua
	// REPARTIÇÃO por leitor (AOS-459). O global já existia como constante do binário e **não era
	// afinável por ambiente** — lacuna que este ticket fecha ao mesmo tempo: validar «o por-leitor é
	// estritamente menor que o global» sem poder configurar o global deixaria o par inútil.
	trajMaxConns          int
	trajMaxConnsPerReader int
	// inFlightPerCaller é o tecto de SUBMISSÕES NOVAS simultâneas por SUBMISSOR (AOS-456) — e não
	// de OCUPAÇÃO: as isenções (suspenso, retoma) compõem-se e um submissor pode ter mais runs vivos
	// do que este número. 0 ⇒ NÃO COMPOSTO, e o banner declara-o.
	inFlightPerCaller int
	// planMaxPending / planMaxPendingPerSubmitter são o tecto de pedidos de plano por drenar e a sua
	// REPARTIÇÃO por submissor (AOS-464). O global existia como uma constante de `plan_claim.go` e não
	// era afinável — a mesma lacuna que o eixo SSE tinha, fechada pela mesma razão: validar «o
	// por-submissor é estritamente menor que o global» sem poder configurar o global deixaria o par
	// inútil.
	planMaxPending             int
	planMaxPendingPerSubmitter int
	// planMaxGenerations é o tecto de gerações de planeamento por pedido (AOS-467).
	planMaxGenerations int
	// apiMaxConns é o tecto de ligações abertas no listener da API (AOS-465), e tem de exceder o
	// tecto de streams SSE: cada stream segura uma ligação durante minutos.
	apiMaxConns int
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
		ratePerSec:                 DefaultRatePerSec,
		burst:                      DefaultRateBurst,
		maxInFlight:                DefaultMaxInFlight,
		planMaxPending:             DefaultPlanMaxPending,
		planMaxPendingPerSubmitter: DefaultPlanMaxPendingPerSubmitter,
		planMaxGenerations:         DefaultPlanMaxGenerations,
		apiMaxConns:                DefaultMaxAcceptedConns,
		readRatePerSec:             DefaultReadRatePerSec,
		readBurst:                  DefaultReadRateBurst,
		trajMaxConns:               DefaultMaxTrajectoryConns,
		trajMaxConnsPerReader:      DefaultMaxTrajectoryConnsPerReader,
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
		// ESTRITAMENTE MENOR, e a IGUALDADE também é recusada — achado da segunda revisão
		// adversarial. A primeira versão aceitava `== global` e o teste declarava-o «coerente, degenera
		// no global». Medido: com global=3 e per-caller=3, a 4.ª submissão do mesmo chamador dá 429
		// **igual com e sem o tecto por-chamador composto** — porque o check global corre no
		// `handleSubmit`, ANTES do `submit`. O por-chamador só dispararia na janela de corrida do check
		// global (que é um TOCTOU fora do mutex). É a MESMA razão que recusa «acima»: uma barreira que
		// não morde, anunciada como LIGADA, é a forma de falha que este ticket existe para não repetir.
		if n >= lim.maxInFlight {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_MAX_INFLIGHT_PER_CALLER=%q tem de ser ESTRITAMENTE MENOR que AOS_INGRESS_MAX_INFLIGHT=%d — igual ou acima do global a barreira por-chamador nao morde (o tecto global e verificado no handler, ANTES do submit) e seria anunciada como LIGADA sem o estar", ErrBadIngressLimits, rawPerCaller, lim.maxInFlight)
		}
		lim.inFlightPerCaller, lim.tuned = n, true
	}

	// AOS-458 — A TAXA DO PLANO DE DADOS INTEIRO, as LEITURAS incluídas.
	//
	// Até este ticket as sete rotas de leitura que chamam `readGovernance.authorize` não tinham
	// tecto de taxa nenhum, e em produção esse `authorize` verifica um JWS. Ver
	// [DefaultReadRatePerSec]. Mesma disciplina fail-closed das outras: valor ilegível, não-finito,
	// negativo ou ZERO aborta o arranque.
	rawReadRate := strings.TrimSpace(os.Getenv("AOS_INGRESS_READ_RATE"))
	if rawReadRate != "" {
		v, ok := parsePositiveFloat(rawReadRate, 0)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_READ_RATE=%q", ErrBadIngressLimits, rawReadRate)
		}
		lim.readRatePerSec, lim.tuned = v, true
	}
	rawReadBurst := strings.TrimSpace(os.Getenv("AOS_INGRESS_READ_BURST"))
	if rawReadBurst != "" {
		v, ok := parsePositiveFloat(rawReadBurst, 1)
		if !ok {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_INGRESS_READ_BURST=%q", ErrBadIngressLimits, rawReadBurst)
		}
		lim.readBurst, lim.tuned = v, true
	}

	// AOS-459 — O TECTO DE STREAMS SSE E A SUA REPARTIÇÃO POR LEITOR.
	//
	// O global lê-se PRIMEIRO, porque a validação do por-leitor depende dele.
	rawTraj := strings.TrimSpace(os.Getenv("AOS_TRAJECTORY_MAX_CONNS"))
	if rawTraj != "" {
		n, err := strconv.Atoi(rawTraj)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_TRAJECTORY_MAX_CONNS=%q", ErrBadIngressLimits, rawTraj)
		}
		lim.trajMaxConns, lim.tuned = n, true
	}
	rawTrajPer := strings.TrimSpace(os.Getenv("AOS_TRAJECTORY_MAX_CONNS_PER_READER"))
	if rawTrajPer != "" {
		n, err := strconv.Atoi(rawTrajPer)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_TRAJECTORY_MAX_CONNS_PER_READER=%q", ErrBadIngressLimits, rawTrajPer)
		}
		lim.trajMaxConnsPerReader, lim.tuned = n, true
	}
	// TECTO DE LIGAÇÕES ACEITES (AOS-465), validado contra o tecto SSE sobre o PAR FINAL — lido depois
	// dele, porque a validação depende do valor em vigor e não só do definido.
	rawConns := strings.TrimSpace(os.Getenv("AOS_API_MAX_CONNS"))
	if rawConns != "" {
		n, err := strconv.Atoi(rawConns)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_API_MAX_CONNS=%q", ErrBadIngressLimits, rawConns)
		}
		lim.apiMaxConns, lim.tuned = n, true
	}
	if lim.apiMaxConns <= lim.trajMaxConns {
		return ingressLimits{}, nil, fmt.Errorf("%w: AOS_API_MAX_CONNS=%d (%s) tem de ser ESTRITAMENTE MAIOR que AOS_TRAJECTORY_MAX_CONNS=%d (%s) — cada stream SSE segura uma ligacao, e sem folga os streams ocupam o listener inteiro, incluindo as ligacoes do plano de CONTROLO (/steer, /pause)",
			ErrBadIngressLimits,
			lim.apiMaxConns, origemDoLimite(rawConns),
			lim.trajMaxConns, origemDoLimite(rawTraj))
	}

	// TECTO DA FILA DE PLANOS (AOS-464) — o global e a sua repartição por submissor.
	//
	// O global lê-se PRIMEIRO, porque a validação da repartição depende dele. Ler os dois na mesma
	// função (e não em `plan_ingress.go`) é o que mantém o banner e a postura ligada derivados do
	// MESMO valor de retorno.
	rawPlan := strings.TrimSpace(os.Getenv("AOS_PLAN_MAX_PENDING"))
	if rawPlan != "" {
		n, err := strconv.Atoi(rawPlan)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_PLAN_MAX_PENDING=%q", ErrBadIngressLimits, rawPlan)
		}
		lim.planMaxPending, lim.tuned = n, true
	}
	rawPlanPer := strings.TrimSpace(os.Getenv("AOS_PLAN_MAX_PENDING_PER_SUBMITTER"))
	if rawPlanPer != "" {
		n, err := strconv.Atoi(rawPlanPer)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_PLAN_MAX_PENDING_PER_SUBMITTER=%q", ErrBadIngressLimits, rawPlanPer)
		}
		lim.planMaxPendingPerSubmitter, lim.tuned = n, true
	}
	// TECTO DE GERAÇÕES POR PEDIDO (AOS-467). 0 não desliga — abortaria nada: deixar por definir
	// é o default. Um pedido cuja decomposição falha sempre de forma transitória re-planeava sem fim.
	rawGer := strings.TrimSpace(os.Getenv("AOS_PLAN_MAX_GENERATIONS"))
	if rawGer != "" {
		n, err := strconv.Atoi(rawGer)
		if err != nil || n <= 0 {
			return ingressLimits{}, nil, fmt.Errorf("%w: AOS_PLAN_MAX_GENERATIONS=%q (inteiro > 0; por definir usa %d)", ErrBadIngressLimits, rawGer, DefaultPlanMaxGenerations)
		}
		lim.planMaxGenerations, lim.tuned = n, true
	}
	// O PAR FINAL, fora dos dois ramos — é literalmente a correcção do AOS-463, aplicada ao nascer
	// deste eixo em vez de paga em revisão adversarial. Se esta comparação vivesse dentro do ramo do
	// por-submissor, baixar só `AOS_PLAN_MAX_PENDING` para `<= 125` deixaria a repartição INERTE em
	// silêncio, e um submissor voltaria a poder ocupar a fila toda.
	if lim.planMaxPendingPerSubmitter >= lim.planMaxPending {
		return ingressLimits{}, nil, fmt.Errorf("%w: AOS_PLAN_MAX_PENDING_PER_SUBMITTER=%d (%s) tem de ser ESTRITAMENTE MENOR que AOS_PLAN_MAX_PENDING=%d (%s) — igual ou acima o tecto global corta primeiro e a reparticao por submissor NUNCA dispara, logo um submissor ocupa a fila toda e nega POST /plans aos outros (tecto inerte anunciado como equidade)",
			ErrBadIngressLimits,
			lim.planMaxPendingPerSubmitter, origemDoLimite(rawPlanPer),
			lim.planMaxPending, origemDoLimite(rawPlan))
	}

	// ESTRITAMENTE MENOR, VALIDADO SOBRE O PAR FINAL — e a validação está aqui FORA dos dois ramos
	// por causa de um fail-open medido (AOS-463).
	//
	// A razão de ser da regra é PRÓPRIA deste eixo, não copiada do AOS-456a: os dois tectos são
	// verificados no MESMO ponto, um após o outro, pelo que com ambos a N um leitor sozinho chega a N
	// sem exceder nenhum e na (N+1)-ésima é o GLOBAL que corta. O por-leitor nunca dispara, logo é
	// inerte — e um tecto inerte anunciado como repartição é a forma de falha que este ciclo de
	// tickets já pagou cinco vezes.
	//
	// O FAIL-OPEN: até ao AOS-463 esta comparação vivia DENTRO do `if rawTrajPer != ""`, pelo que só
	// um par EXPLÍCITO era validado. Baixar apenas `AOS_TRAJECTORY_MAX_CONNS` — a coisa mais natural
	// de fazer num nó pequeno — deixava o por-leitor no default 32 e ninguém comparava nada. Medido:
	// `AOS_TRAJECTORY_MAX_CONNS=4` (e `=32`) ARRANCAVAM com `por-leitor=32 >= global`, repartição
	// INERTE, e um leitor ocupava os quatro lugares e negava `GET /runs/{id}/trajectory` a todos — o
	// DoS que o AOS-459 existe para fechar. A tabela de 14 casos do AOS-459 não o cobria porque todos
	// os casos com `global` explícito punham também o `porLeitor` explícito.
	//
	// O ERRO NOMEIA A ORIGEM DE CADA VALOR (`definida` vs `default`): sem isso o operador que definiu
	// UMA variável lê uma recusa sobre um número que não escreveu.
	if lim.trajMaxConnsPerReader >= lim.trajMaxConns {
		return ingressLimits{}, nil, fmt.Errorf("%w: AOS_TRAJECTORY_MAX_CONNS_PER_READER=%d (%s) tem de ser ESTRITAMENTE MENOR que AOS_TRAJECTORY_MAX_CONNS=%d (%s) — igual ou acima o tecto global corta primeiro e a reparticao por leitor NUNCA dispara, logo um leitor ocupa todos os lugares e nega GET /runs/{id}/trajectory aos outros (tecto inerte anunciado como equidade)",
			ErrBadIngressLimits,
			lim.trajMaxConnsPerReader, origemDoLimite(rawTrajPer),
			lim.trajMaxConns, origemDoLimite(rawTraj))
	}

	return lim, []APIOption{
		WithRateLimit(lim.ratePerSec, lim.burst),
		WithMaxInFlight(lim.maxInFlight),
		WithReadRateLimit(lim.readRatePerSec, lim.readBurst),
		WithMaxTrajectoryConns(lim.trajMaxConns),
		WithMaxTrajectoryConnsPerReader(lim.trajMaxConnsPerReader),
		WithPlanMaxPending(lim.planMaxPending),
		WithPlanMaxPendingPerSubmitter(lim.planMaxPendingPerSubmitter),
		WithPlanMaxGenerations(lim.planMaxGenerations),
		WithMaxAcceptedConns(lim.apiMaxConns),
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

// dobraDoTectoDaFila declara o tecto de pedidos de plano por drenar e a sua REPARTIÇÃO por submissor,
// em QUATRO ramos — no molde dos eixos AOS-456 e AOS-459, e pela mesma razão: um tecto sobre um valor
// que o chamador escolhe não é a mesma coisa que um tecto sobre uma credencial verificada, e o operador
// tem de saber qual tem.
//
// QUATRO, e não três: «não configurada» (a variável desligada) e «configurada mas não composta» (o gate
// ausente, ou presente sem credencial forte) exigem acções DIFERENTES do operador — definir a variável,
// compor o gate, ou compor o OIDC. Colapsá-las mandaria parte deles editar o ficheiro errado. O godoc
// desta função dizia «TRÊS posturas» enquanto o teste e o ticket diziam QUATRO; era a mesma frase em
// dois sítios com números diferentes.
//
// SÃO QUATRO RAMOS E SÓ UM COMPÕE, e é a diferença face aos eixos AOS-456a e AOS-459, que compõem
// sobre um principal DEMO-GRADE. Aqui as duas posturas intermédias recusam compor, por razões
// diferentes e ambas medidas:
//
//   - principal VAZIO (sem gate): um tecto chaveado no vazio valeria como tecto global mais apertado,
//     anunciado como equidade — o corpo do pedido nunca declara o principal;
//   - principal FORJÁVEL (gate sem credencial forte): pior do que contornável. Um atacante escreve o
//     header da VÍTIMA e gasta a quota dela, com ocupação DURÁVEL e gratuita. Medido: 5 pedidos
//     forjados fecham uma vítima com 15 de 20 lugares livres. Seria um trinco de negação dirigida
//     anunciado como equidade. Ver [handlePlanRequest] para o porquê de a durabilidade ser o que
//     distingue este eixo dos gémeos.
//
// A CONJUNÇÃO É EXPLÍCITA no ramo VERIFICADO pela razão que o AOS-461 pagou: hoje `principalVerificavel`
// implica `gateComposto`, mas esta função não pode depender dessa implicação para estar certa.
//
// O NOME DA DOBRA SEGUE A CONVENÇÃO «TECTO … (AOS-NNN):» de propósito, e está registado em
// `marcadoresDeDobra`: uma dobra fora da convenção é invisível ao guarda que vigia o registo e fica
// engolida pela dobra anterior — foi o achado BAIXO-3/MÉDIO-4 da oitava e nona revisões, e
// reintroduzi-lo aqui seria pagá-lo uma terceira vez.
func dobraDoTectoDaFila(lim ingressLimits, gateComposto, principalVerificavel bool) string {
	d := fmt.Sprintf(" TECTO DA FILA DE PLANOS (AOS-464): %d pedidos por drenar; atingido, POST /plans RECUSA pedidos novos com 503 e NUNCA descarta os antigos (AOS-423, ADR-030 2.7).", lim.planMaxPending)
	switch {
	case lim.planMaxPendingPerSubmitter <= 0:
		d += fmt.Sprintf(" REPARTICAO POR SUBMISSOR: NAO CONFIGURADA — AOS_PLAN_MAX_PENDING_PER_SUBMITTER desligada (<=0), logo o tecto e SO global e UM submissor autenticado enfileira os %d e nega POST /plans a todos os outros ate alguem drenar.", lim.planMaxPending)
	case !gateComposto:
		d += fmt.Sprintf(" REPARTICAO POR SUBMISSOR: CONFIGURADA (%d) mas NAO COMPOSTA — sem gate soberano de leitura o principal do pedido fica VAZIO para TODOS os chamadores (o corpo nunca o declara), nao ha a quem imputar, e a reparticao fica DESLIGADA em vez de degenerar num tecto global mais apertado. Defina AOS_BOARD_REGIONS (e o WORM).", lim.planMaxPendingPerSubmitter)
	case !principalVerificavel:
		d += fmt.Sprintf(" REPARTICAO POR SUBMISSOR: CONFIGURADA (%d) e NAO COMPOSTA — o gate soberano esta composto mas SEM credencial forte, logo o submissor vem do header X-Aos-Reader que o CHAMADOR escreve. Este tecto NAO se compoe nessa postura, e a razao e mais forte do que «contorna-se»: um atacante escreveria o header da VITIMA e gastaria a quota DELA, fechando-a fora de POST /plans com o resto da fila LIVRE (medido: 5 pedidos forjados, 15 de 20 lugares livres), com ocupacao DURAVEL e gratuita — um pedido so sai da fila com desfecho terminal ou reclamacao viva. Ligar a reparticao aqui seria entregar um trinco de negacao DIRIGIDA em vez de equidade. O tecto global de %d continua a ser a unica barreira. Para a compor defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE.", lim.planMaxPendingPerSubmitter, lim.planMaxPending)
	default:
		d += fmt.Sprintf(" REPARTICAO POR SUBMISSOR: LIGADA sobre principal VERIFICADO — cada submissor ocupa no maximo %d de %d pedidos por drenar; a atribuicao vem de credencial FORTE verificada (OIDC), logo nao e forjavel. Exceder responde 429 (o chamador tem de drenar o que e dele) e NAO 503 (o no sem consumidor), e a reparticao e verificada ANTES do tecto global para que o diagnostico aponte a causa certa.", lim.planMaxPendingPerSubmitter, lim.planMaxPending)
	}
	return d
}

// dobraDasGeracoes declara o tecto de gerações de planeamento por pedido (AOS-467), e o que ele NÃO
// conta. O nome segue a convenção «TECTO … (AOS-NNN):» e está em `marcadoresDeDobra`.
func dobraDasGeracoes(lim ingressLimits) string {
	return fmt.Sprintf(" TECTO DE GERACOES POR PEDIDO (AOS-467): %d geracoes de planeamento por pedido de POST /plans (AOS_PLAN_MAX_GENERATIONS). Conta cada geracao cuja anterior NAO acabou a espera de humano — as re-ofertas depois de um desfecho transitorio ou de uma reclamacao expirada; as re-verificacoes de um plano a espera de humano nao contam (essas tem o prazo de 24 h do aos-orq). A geracao que passa o tecto e entregue MARCADA, sem verificar quota nem abrir o objectivo, e o aos-orq fecha o pedido com a saida 12 SEM planear. Um aos-orq anterior ao AOS-467 ignora a marca e planeia na mesma.", lim.planMaxGenerations)
}

// dobraDasLigacoes declara o tecto de ligações aceites, o critério do despejo, e QUANTAS ligações os
// streams SSE não conseguem tomar. É esse o número que importa ao operador: o plano de controlo
// (/steer, /pause) disputa as mesmas vagas. E declara o que o tecto NÃO faz — duas revisões
// adversariais mediram frases deste banner falsas («ficam N para o plano de controlo», «uma ligacao
// que so segura a vaga nao as tira a um pedido novo»), e é a frase que o operador lê.
func dobraDasLigacoes(lim ingressLimits) string {
	return fmt.Sprintf(" TECTO DE LIGACOES ACEITES (AOS-465): %d ligacoes TCP abertas no listener; atingido, uma ligacao NOVA DESPEJA outra em vez de esperar, e so protege a vaga um handler a fazer trabalho do SERVIDOR — ler o corpo, escrever a resposta ou ter a resposta por terminar e esperar pelo CLIENTE e nao protege. Sai primeiro a ociosa ou sem pedido, depois a que espera pelo cliente ha mais tempo, em regra nenhuma antes de %s no estado actual; uma ligacao nova espera no maximo esse prazo desde que haja uma candidata, e so espera mais enquanto TODAS estiverem a trabalhar. Os streams SSE entre eventos estao a trabalhar e tomam no maximo %d; as outras %d servem o resto da API, incluindo o plano de CONTROLO. NAO contem uma inundacao volumetrica de ligacoes ou de pedidos, em que a ligacao nova pode despejar uma resposta acabada de terminar, nem o trabalho legitimo dentro de handlers (esse e dos tectos acima deste): isso e do edge (limit_conn).",
		lim.apiMaxConns, GracaDeDespejo, lim.trajMaxConns, lim.apiMaxConns-lim.trajMaxConns)
}

// origemDoLimite diz se um valor veio da variável de ambiente ou do default do binário. Existe para
// que a recusa do par inerte nomeie a origem de cada número: um operador que definiu UMA das duas
// variáveis precisa de saber que o outro valor é um default, senão depura o número errado.
//
// FRONTEIRA MEDIDA (achado BAIXO-1 da nona revisão): no deployment recomendado este helper quase nunca
// dirá «default do binario». O `deploy/server/docker-compose.prod.yml` exporta SEMPRE as duas variáveis
// (`${…:-256}`/`${…:-32}`), pelo que quem baixa só o global no `.env` lê «PER_READER=32 (definida)» — um
// 32 que não escreveu em sítio nenhum. O propósito do helper é derrotado exactamente onde mais faria
// falta, e a correcção é no compose, não aqui.
//
// E O QUARTO CASO É VÁCUO: com nenhuma das duas definida o par é 32 < 256, o abort não dispara, e
// `(default, default)` nunca chega à mensagem. São três casos alcançáveis, não quatro.
//
// A POSIÇÃO DESTA FUNÇÃO É LOAD-BEARING: o AOS-463 inseriu-a entre o godoc de [ingressPostureBanner] e
// a função, sem linha em branco, e o maior documento de contrato deste ficheiro passou a ser o godoc
// deste helper de quatro linhas — enquanto [ingressPostureBanner] ficava com ZERO. Quinta ocorrência da
// classe nesta sessão, cometida no commit que declarou que corrigir a instância e não a classe garantia
// uma quinta. O sensor está em [TestAOS462NenhumGodocSequestrado].
func origemDoLimite(raw string) string {
	if raw != "" {
		return "definida"
	}
	return "default do binario"
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
//   - COBRE as LEITURAS desde AOS-458, por um balde SEPARADO (`AOS_INGRESS_READ_RATE`/
//     `AOS_INGRESS_READ_BURST`), consumido no INVÓLUCRO da rota e portanto antes de qualquer
//     verificação criptográfica no corpo. Antes disso `GET /runs/{id}` e as outras seis rotas de
//     leitura não tinham tecto de taxa NENHUM — e todas chamam `readGovernance.authorize`, que em
//     produção verifica um JWS. Esta linha dizia «NÃO cobre as LEITURAS», e foi a refutação do
//     argumento com que o AOS-456b se fechou: estava escrita aqui e ninguém a leu.
//     O stream SSE de trajectória tem AINDA o seu próprio tecto de LIGAÇÕES, agora em DUAS camadas
//     (AOS-459): o global ([DefaultMaxTrajectoryConns], `AOS_TRAJECTORY_MAX_CONNS`) que protege o
//     nó, e a REPARTIÇÃO por leitor (`AOS_TRAJECTORY_MAX_CONNS_PER_READER`) que impede um leitor
//     autenticado de ocupar todos os lugares e negar a rota aos outros. Era o resíduo declarado no
//     AOS-458, e está fechado; a repartição imputa ao principal que o gate soberano resolveu, e em
//     modo legado (principal vazio) degenera no global.
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
	origem := "nos DEFAULTS do binario (nenhuma de AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT/AOS_INGRESS_MAX_INFLIGHT_PER_CALLER/AOS_INGRESS_READ_RATE/AOS_INGRESS_READ_BURST/AOS_TRAJECTORY_MAX_CONNS/AOS_TRAJECTORY_MAX_CONNS_PER_READER definida)"
	if lim.tuned {
		origem = "AFINADO por uma ou mais de AOS_INGRESS_RATE/AOS_INGRESS_BURST/AOS_INGRESS_MAX_INFLIGHT/AOS_INGRESS_MAX_INFLIGHT_PER_CALLER/AOS_INGRESS_READ_RATE/AOS_INGRESS_READ_BURST/AOS_TRAJECTORY_MAX_CONNS/AOS_TRAJECTORY_MAX_CONNS_PER_READER"
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
	alcance := " ALCANCE do tecto por-chamador: admite N submissoes NOVAS simultaneas — NAO e um tecto de OCUPACAO: um run SUSPENSO a espera de aval humano SAI da contagem e a RETOMA (/resume) NAO a consulta, pelo que um submissor pode ter MAIS de N runs em `s.runs` (medido: 20 com o tecto a 1). E o 429 por-chamador GASTA um token do balde GLOBAL (o balde e consumido no topo do handler, a decisao por-chamador no submit): a rajada de um chamador nao tira LUGARES aos outros, mas gasta TAXA comum — justica em TAXA e o eixo AOS-456b, NAO esta feita. PISO PRATICO: o aos-orq despacha ate 16 runs-filho em paralelo por plano, e a imputacao e ao HUMANO que pediu o plano — nao ao plano. Logo DOIS planos concorrentes do mesmo humano PARTILHAM este tecto, e o piso real e 16 x (planos concorrentes do mesmo humano), nao 16. Um valor abaixo disso recusa filhos de um plano cujo fan-out SIMULTANEO o exceda. E com `aos-orq serve` manual (sem geracao de pedido) nao ha submissor derivado: TODOS os filhos sao imputados ao principal do proprio aos-orq, que passa a ter um tecto unico para tudo o que despacha."
	switch {
	// A CONJUNÇÃO É EXPLÍCITA (achado da segunda revisão adversarial): sem `gateComposto` o
	// `serveAPI` NÃO compõe o tecto, pelo que anunciar VERIFICADO seria anunciar uma barreira
	// inexistente. Hoje `principalDoRunEVerificavel ⇒ noTemGateSoberanoDeLeitura` torna a combinação
	// inalcançável, mas esta função não pode depender disso para estar certa — era exactamente a
	// forma do ALTO-1b (um predicado a confiar numa coincidência de outro sítio).
	case lim.inFlightPerCaller > 0 && gateComposto && principalVerificavel:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): LIGADO sobre principal VERIFICADO — cada submissor admite no maximo %d submissao(oes) NOVA(s) simultanea(s); exceder responde 429 sem ocupar lugar nenhum. A atribuicao vem de credencial FORTE verificada (OIDC), logo nao e forjavel pelo chamador.%s",
			lim.inFlightPerCaller, alcance)
	case lim.inFlightPerCaller > 0 && gateComposto:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): LIGADO sobre principal DEMO-GRADE (%d) — ATENCAO: sem credencial forte composta o principal vem do header X-Aos-Reader, que o CHAMADOR escreve, pelo que este tecto CONTORNA-SE rodando o header (medido: 60 submissoes rotativas, 60 admitidas com o tecto a 2). Vale contra rajada HONESTA ou cliente mal configurado; NAO vale contra abuso. Para o tornar inforjavel defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE (AOS_MODE=production ja os exige).%s",
			lim.inFlightPerCaller, alcance)
	case lim.inFlightPerCaller > 0:
		porChamador = fmt.Sprintf(" TECTO POR-CHAMADOR (AOS-456): CONFIGURADO (%d) mas NAO COMPOSTO — sem gate soberano de leitura o principal do run vem do CORPO do pedido (auto-declarado), e um tecto sobre um valor que o chamador escolhe contorna-se mudando-o. NAO esta em vigor: defina AOS_BOARD_REGIONS (e o WORM).",
			lim.inFlightPerCaller)
	}
	// DOBRA DO TECTO DE STREAMS SSE POR LEITOR (AOS-459/AOS-460) — TRÊS posturas, no molde do
	// AOS-456, e a do meio é a que a revisão adversarial apanhou OUTRA VEZ: o AOS-459 descrevia-a
	// como se fosse a primeira («em modo legado o principal vem vazio»), quando é a postura de um nó
	// com `AOS_BOARD_REGIONS` e sem OIDC — o principal vem do header `X-Aos-Reader` e a repartição
	// contorna-se rodando-o. Medido: 12 streams vivos com o tecto a 1, só a rodar o header.
	porLeitorSSE := fmt.Sprintf(" TECTO DE STREAMS SSE POR LEITOR (AOS-459): NAO COMPOSTO — AOS_TRAJECTORY_MAX_CONNS_PER_READER desligado (<=0), logo o tecto de %d stream(s) e SO global e um leitor pode ocupar todos e negar GET /runs/{id}/trajectory aos outros.", lim.trajMaxConns)
	switch {
	// A CONJUNÇÃO É EXPLÍCITA, pela mesma razão que o switch do AOS-456 acima a tem: hoje
	// `principalDoRunEVerificavel ⇒ noTemGateSoberanoDeLeitura`, mas esta função não pode depender
	// dessa implicação para estar certa. Sem o `gateComposto` este ramo anuncia «LIGADO sobre
	// principal VERIFICADO … credencial FORTE» num nó onde o `admitSovereignRead` devolve principal
	// VAZIO e NÃO há repartição nenhuma — duplamente falso, e é literalmente a forma do ALTO-1b que
	// o AOS-456a fechou. Achado MÉDIO-1 da sétima revisão adversarial; o caso está na tabela de
	// [TestAOS460OBannerDeclaraAsTRESPosturasDoTectoPorLeitor] como «verificavel SEM gate».
	case lim.trajMaxConnsPerReader > 0 && gateComposto && principalVerificavel:
		porLeitorSSE = fmt.Sprintf(" TECTO DE STREAMS SSE POR LEITOR (AOS-459): LIGADO sobre principal VERIFICADO — cada leitor ocupa no maximo %d de %d stream(s) concorrentes; a atribuicao vem de credencial FORTE verificada (OIDC), logo nao e forjavel. A recusa por-leitor NAO toma lugar global nenhum (AOS-460: a reparticao corre ANTES do tecto global, logo uma rajada de recusas de um leitor deixa de negar a rota aos outros pelo tecto GLOBAL). NAO promete que o outro leitor nao leve 429: dentro da sua quota um leitor ocupa lugares globais legitimamente, e um tecto por-leitor a 1 faz o proprio leitor colidir com o seu lugar ainda nao libertado — o que desaparece a 8 e a 32 (AOS-461). As gamas medidas ficam no ticket e no cabecalho do teste, com a CONDICAO declarada (mesmo contentor, carga diferente — nao ha maquina, cores nem GOMAXPROCS declarados): sao contagens sob contencao e mudam com a carga, logo nao pertencem a um banner.", lim.trajMaxConnsPerReader, lim.trajMaxConns)
	case lim.trajMaxConnsPerReader > 0 && gateComposto:
		porLeitorSSE = fmt.Sprintf(" TECTO DE STREAMS SSE POR LEITOR (AOS-459): LIGADO sobre principal DEMO-GRADE (%d de %d) — ATENCAO: sem credencial forte composta o leitor vem do header X-Aos-Reader, que o CHAMADOR escreve, pelo que este tecto CONTORNA-SE rodando o header (medido: 12 streams vivos com o tecto a 1). Vale contra rajada HONESTA ou cliente mal configurado; NAO vale contra abuso. Para o tornar inforjavel defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE.", lim.trajMaxConnsPerReader, lim.trajMaxConns)
	case lim.trajMaxConnsPerReader > 0:
		porLeitorSSE = fmt.Sprintf(" TECTO DE STREAMS SSE POR LEITOR (AOS-459): CONFIGURADO (%d) mas NAO COMPOSTO — sem gate soberano de leitura o `admitSovereignRead` devolve principal VAZIO, nao ha a quem imputar, e a reparticao degenera no tecto global de %d. Defina AOS_BOARD_REGIONS (e o WORM).", lim.trajMaxConnsPerReader, lim.trajMaxConns)
	}
	// INVARIANTE INERTE — E A HISTÓRIA DESTE RAMO, que é o próprio objecto de três tickets.
	//
	// O AOS-460 acrescentou-o e declarou-o como correcção entregue ao operador. O AOS-461 disse o
	// contrário — que «nenhuma configuração por ambiente o alcança», logo o operador nunca o vê — e
	// rebaixou-o a cinto-e-suspensórios. **As duas afirmações estavam erradas, e a segunda tapava um
	// fail-open:** até ao AOS-463 a comparação `por-leitor < global` vivia DENTRO do ramo do
	// por-leitor, pelo que `AOS_TRAJECTORY_MAX_CONNS=4` sozinho ARRANCAVA com o por-leitor no default
	// 32, repartição INERTE — e este aviso era a ÚNICA coisa que o dizia ao operador, precisamente
	// enquanto o AOS-461 o declarava inalcançável. Ver a nota do abort em [ingressLimitsFromEnv].
	//
	// HOJE, depois de o AOS-463 validar o PAR FINAL: o abort cobre todos os estados alcançáveis por
	// ambiente (17 casos em [TestAOS459EnvFailClosedEORRACIOENTREOSDOIS]), e é ele a barreira que morde.
	//
	// E A RAZÃO PELA QUAL O RAMO FICA — terceira tentativa, porque a segunda também estava errada
	// (achado MÉDIO-3 da nona revisão). Ela dizia «fica para a composição in-process, que não passa por
	// essa validação». **Falso, e medido:** esta função tem UM chamador de produção (`main.go`,
	// alimentado pelo `ingressLim` lido do ambiente e nunca reatribuído), o `ingressLimits` é
	// não-exportado, e [WithMaxTrajectoryConns]/[WithMaxTrajectoryConnsPerReader] configuram o
	// `apiHandler.cfg` — nunca um `ingressLimits`. A composição in-process **não chega ao banner de
	// todo**. Logo o ramo é hoje inalcançável pelos DOIS caminhos, e a conclusão do AOS-461 («um
	// operador nunca vê este texto») voltou a ser verdadeira — por causa do abort do AOS-463, não por
	// já o ser quando ele a escreveu.
	//
	// O ramo fica porque está FIXADO EM TESTE (`TestAOS460OBannerDECLARAUmParINERTE`): se alguém
	// alargar a validação da env, ou der ao banner outra fonte de limites, o texto tem de continuar a
	// existir e a dizer a verdade. É uma asserção sobre a função, não a protecção de um caminho de
	// composição. A lição, à terceira: «este ramo é inalcançável» e «este ramo protege X» são ambas
	// afirmações sobre TODOS os caminhos de entrada — a espécie que este ciclo já errou três vezes.
	if lim.trajMaxConnsPerReader > 0 && lim.trajMaxConnsPerReader >= lim.trajMaxConns {
		porLeitorSSE += fmt.Sprintf(" ATENCAO: o tecto por-leitor (%d) NAO e menor que o global (%d), logo o global corta primeiro e a reparticao NUNCA dispara — esta INERTE.", lim.trajMaxConnsPerReader, lim.trajMaxConns)
	}

	return []string{
		fmt.Sprintf("ingresso / admission (AOS-166/AOS-277/AOS-458): LIGADO e %s — POST /runs admite %.4g pedido(s)/segundo com burst de %.4g e no maximo %d run(s) EM CURSO nesta replica; exceder qualquer um responde 429. ALCANCE: cobre POST /runs e SO — o plano de CONTROLO (/steer,/pause,/approve,/resume) tem um balde DEDICADO que estas variaveis NAO afinam, as leituras (GET /runs/{id} e o resto do plano de DADOS) tem desde AOS-458 um balde de TAXA proprio (AOS_INGRESS_READ_RATE/AOS_INGRESS_READ_BURST) consumido no involucro da rota, e o stream SSE de trajectoria tem AINDA um tecto de LIGACOES em duas camadas (AOS_TRAJECTORY_MAX_CONNS global + AOS_TRAJECTORY_MAX_CONNS_PER_READER por leitor). O balde e POR-PROCESSO, em memoria e GLOBAL entre chamadores: NAO e por-IP nem por-principal (um so cliente ruidoso pode esgota-lo para todos) e N replicas valem N vezes este limite — nao ha limite de admissao agregado no cluster. O tecto de in-flight conta os runs REGISTADOS no loop de servico: um run SUSPENSO a espera de aval humano SAI dessa contagem e NAO ocupa lugar, e a RETOMA (/resume) re-hospeda SEM consultar o tecto. O 429 nao leva Retry-After.%s",
			origem, lim.ratePerSec, lim.burst, lim.maxInFlight,
			porChamador+porLeitorSSE+dobraDoTectoDaFila(lim, gateComposto, principalVerificavel)+dobraDasGeracoes(lim)+dobraDasLigacoes(lim)),
	}
}
