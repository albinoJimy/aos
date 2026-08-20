package main

import (
	"errors"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// PLANOS e TABELA DE ROTAS — a classificação de cada rota, e as barreiras que dela decorrem.
//
// PORQUE ISTO EXISTE (e não é organização gratuita).
//
// Antes, cada handler chamava `admitControl`/`admitControlMTLS` no seu próprio corpo. Uma barreira
// imposta por CONVENÇÃO DE ESCRITA tem três propriedades que, juntas, a tornam invisível quando
// falha:
//
//  1. nada estrutural liga "isto é plano de controlo" a "isto tem as barreiras" — é memória de
//     quem escreve;
//
//  2. `admitControlMTLS` devolve `true` quando o mTLS não está composto, que é o caso em dev, em
//     CI e na produção actual. Um handler que a ESQUEÇA comporta-se de forma IDÊNTICA a um que a
//     chame, em todos os testes e em toda a operação. O sinal só aparece no dia em que alguém liga
//     o mTLS — e aparece como AUSÊNCIA de uma recusa, que ninguém observa;
//
//  3. a verificação era um allowlist escrito à mão. Esquecer uma entrada produzia silêncio, tal
//     como esquecer a chamada: o detector partilhava o modo de falha do defeito que perseguia.
//
// As rotas de autonomia nasceram sem as duas barreiras enquanto o plano e o comentário da própria
// rota afirmavam que passavam "pela mesma admissão do /approve". O teste que se escreveu a seguir
// verificou só metade, e ficou verde. E a lista desse teste não incluía uma única rota de DSAR.
//
// A CORRECÇÃO é trocar um conjunto ABERTO por um FECHADO. Uma rota HTTP só existe se estiver
// registada; o registo é, portanto, o único sítio por onde toda a rota passa OBRIGATORIAMENTE. É
// aí que se declara o plano, e é daí que as barreiras derivam — deixam de ser repetidas à mão.
//
// O valor-zero de [plano] é INVÁLIDO e ABORTA O ARRANQUE. É o mesmo idioma do resto do sistema
// (`ClassDanger = iota`, `SensitivityUnknown` a resolver para o topo): uma rota nova sem plano
// declarado não falha um teste que alguém tem de se lembrar de correr — recusa arrancar.
// ---------------------------------------------------------------------------

// plano é o PLANO a que uma rota pertence, e é o que DECIDE as barreiras que ela atravessa.
type plano uint8

const (
	// planoPorClassificar é o VALOR-ZERO, e é inválido de propósito: uma entrada da tabela
	// escrita sem plano (`{padrao: "...", handler: ...}` compila com o campo a zero) aborta o
	// arranque em vez de ficar silenciosamente sem barreiras.
	planoPorClassificar plano = iota

	// planoAberto — SEM autenticação e SEM admission, por decisão explícita: sondas de
	// orquestrador e scrape de métricas são frequentes e não assinam; passá-las pelo
	// token-bucket causaria falso-unready. Restringe-se por REDE, não por credencial.
	planoAberto

	// planoDados — o read/write path dos runs.
	//
	// NÃO aplica barreira no invólucro, e a razão é honesta: o balde de dados (`h.bucket`)
	// pertence só à SUBMISSÃO, e `admitSovereignRead` devolve a identidade do leitor que o
	// CORPO do handler consome. Nenhuma das duas reduz a um invólucro sem mudar assinaturas ou
	// sem passar a limitar rotas que hoje não são limitadas.
	//
	// A classificação vale à mesma: impede que uma rota de dados seja tratada como aberta ou
	// como controlo por distracção, e o teste de planos verifica que ela não escorrega.
	planoDados

	// planoGovernacao — DSAR e legal hold: admission do plano de controlo + credencial FORTE
	// de governação verificada NO CORPO (`readGov.authorize`, asserção OIDC com claim de board).
	//
	// SEM o mTLS de transporte, e isso é hoje uma DECISÃO EM ABERTO, escrita aqui em vez de
	// ficar por omissão. Ver o comentário de [barreirasDe] — é lá que a mudar, se se mudar.
	planoGovernacao

	// planoControlo — tudo o que MUDA ou EXPÕE o estado de governação do nó. Admission +
	// mTLS do plano de controlo, ambos ANTES do handler, e a assinatura ed25519 do corpo
	// (AOS-160) a decidir depois, dentro dele.
	planoControlo
)

func (p plano) String() string {
	switch p {
	case planoAberto:
		return "aberto"
	case planoDados:
		return "dados"
	case planoGovernacao:
		return "governacao"
	case planoControlo:
		return "controlo"
	default:
		return "por-classificar"
	}
}

// ErrRotaPorClassificar aborta o arranque quando uma entrada da tabela de rotas não declara o
// plano. Fail-closed: o nó recusa servir uma superfície cujas barreiras ninguém decidiu.
var ErrRotaPorClassificar = errors.New("aos: rota registada SEM plano declarado — o valor-zero de `plano` e invalido de proposito; declare planoAberto, planoDados, planoGovernacao ou planoControlo na tabela de rotas (ver planos.go)")

// rota é uma entrada da tabela de registo: o padrão método+caminho da stdlib, o handler, e o
// PLANO — que não é decorativo, é o que aplica as barreiras.
type rota struct {
	padrao  string
	handler http.HandlerFunc
	plano   plano
}

// barreirasDe envolve um handler com as barreiras do seu plano.
//
// A ORDEM importa e é a que os handlers tinham: `admitControl` (barato, token-bucket) ANTES de
// `admitControlMTLS` (verificação de cadeia). Rejeitar barato antes de gastar cripto é a postura
// que o banner declara para o ingresso, e o invólucro preserva-a.
//
// DECISÃO EM ABERTO — o mTLS sobre o plano de GOVERNAÇÃO. Hoje `/dsar/erase`, `/dsar/hold`,
// `/dsar/release` e `/dsar/expire` NÃO exigem certificado de cliente: autenticam-se pela asserção
// OIDC verificada (`readGov.authorize`), que identifica o principal de forma pelo menos tão forte.
// Falta-lhes a barreira de TRANSPORTE, e falta exactamente onde a acção é menos reversível — o
// `/dsar/erase` é o crypto-shred, a única operação do nó que nenhum restore drill desfaz.
//
// Promover a governação a mTLS é acrescentar `h.admitControlMTLS` ao ramo `planoGovernacao`
// abaixo. NÃO se fez porque não é uma mudança de código: no dia em que o mTLS for ligado, um
// operador DSAR com asserção válida passa a receber 403 até ter certificado de cliente, o que
// compromete a organização a emitir PKI de cliente a esses operadores — a provisão que o DEF-012
// defere explicitamente para fora do nó.
func (h *apiHandler) barreirasDe(p plano, next http.HandlerFunc) http.HandlerFunc {
	switch p {
	case planoGovernacao:
		return func(w http.ResponseWriter, r *http.Request) {
			if !h.admitControl(w) {
				return
			}
			next(w, comMemoLeitor(r))
		}
	case planoControlo:
		return func(w http.ResponseWriter, r *http.Request) {
			if !h.admitControl(w) {
				return
			}
			if !h.admitControlMTLS(w, r) {
				return
			}
			next(w, comMemoLeitor(r))
		}
	default:
		// planoAberto e planoDados: sem barreira de ADMISSÃO no invólucro, pelas razões escritas em
		// cada um — mas o memo do leitor entra na mesma, porque a propriedade que ele defende não é
		// de admissão e vale para TODAS as rotas.
		return func(w http.ResponseWriter, r *http.Request) { next(w, comMemoLeitor(r)) }
	}
}

// registar aplica a tabela ao mux. É o ÚNICO sítio do binário que chama `mux.HandleFunc` — um
// teste impõe-no, e é isso que fecha o último buraco: sem essa restrição, alguém registaria uma
// rota directamente e contornaria a classificação sem que nada avisasse.
func registar(mux *http.ServeMux, h *apiHandler, rotas []rota) error {
	vistos := make(map[string]struct{}, len(rotas))
	for _, rt := range rotas {
		if rt.plano == planoPorClassificar {
			return fmt.Errorf("%w: %q", ErrRotaPorClassificar, rt.padrao)
		}
		if rt.handler == nil {
			return fmt.Errorf("aos: rota %q sem handler", rt.padrao)
		}
		// Padrão duplicado faz `mux.HandleFunc` entrar em PANIC no arranque. Aqui devolve-se
		// erro, que o nó trata como qualquer outra falha de composição.
		if _, dup := vistos[rt.padrao]; dup {
			return fmt.Errorf("aos: rota %q registada duas vezes", rt.padrao)
		}
		vistos[rt.padrao] = struct{}{}
		mux.HandleFunc(rt.padrao, h.barreirasDe(rt.plano, rt.handler))
	}
	return nil
}

// tabelaDeRotas é a superfície HTTP COMPLETA do nó, e a ÚNICA. Está aqui, e não embutida no
// registo, para que os testes de planos percorram EXACTAMENTE a mesma lista que serve os
// pedidos — e não uma segunda cópia escrita à mão, que foi o defeito que este ficheiro existe
// para fechar: o teste anterior tinha um allowlist manual, e nenhuma rota de DSAR constava dele.
func (h *apiHandler) tabelaDeRotas() []rota {
	return []rota{
		// SONDAS de orquestrador (AOS-171, E5): liveness + readiness. SEM autenticação (probes
		// de k8s/orquestrador não assinam) e SEM admission/rate-limit (probes são frequentes;
		// passá-las pelo token-bucket causaria falso-unready). NÃO consomem os buckets de
		// admitData/admitControl nem o tecto de trajConns. O padrão método+rota da stdlib
		// (Go 1.22+) já devolve 405 a métodos != GET.
		{"GET /healthz", h.handleHealthz, planoAberto},
		{"GET /readyz", h.handleReadyz, planoAberto},
		// Observabilidade MÉTRICA (revisão de prontidão #5): via de métricas em texto Prometheus. O
		// nó só exportava traces — os SLOs (disponibilidade, saúde de dependências, USE) são métricos e
		// eram indetectáveis. Não-autenticada como healthz/readyz (scrapers não assinam; sem PII/segredos;
		// restringir por rede). Zero-dep: texto emitido à mão, sem SDK de métricas.
		{"GET /metrics", h.handleMetrics, planoAberto},

		// Plano de DADOS. O balde de ingresso (`h.bucket`) fica no corpo do handleSubmit: é da
		// SUBMISSÃO, e movê-lo para o invólucro passaria a limitar as leituras, que hoje não o são.
		{"POST /runs", h.handleSubmit, planoDados},
		{"GET /runs/{id}", h.handleGet, planoDados},
		// Plano de DADOS — read-path TEMPO-REAL (AOS-167): SSE dos eventos da trajectória.
		{"GET /runs/{id}/trajectory", h.handleTrajectory, planoDados},
		// Plano de DADOS — RECONSTRUÇÃO SOBERANA de conteúdo selado (AOS-214): decifra o conteúdo
		// cifrado por-titular (AOS-093) de um run para um leitor autorizado por soberania (D7+D6).
		// Desligado (501) sem o gate soberano composto. Ver sovereign_replay.go.
		{"GET /runs/{id}/reconstruct", h.handleReconstruct, planoDados},

		// Plano de CONTROLO TRUSTED (cada um autenticado na fronteira real do nó: a admissão e o
		// mTLS vêm do invólucro; a assinatura ed25519 do corpo, que é quem DECIDE, fica no handler).
		{"POST /runs/{id}/steer", h.handleSteer, planoControlo},
		{"POST /runs/{id}/pause", h.handlePause, planoControlo},
		{"POST /runs/{id}/approve", h.handleApprove, planoControlo},
		// FRESCURA POR-CERIMÓNIA (AOS-266, achado F10): EMISSÃO server-side do challenge do 4-eyes.
		// É o LADO DE EMISSÃO indivisível da porta integration.WithChallengeIssuance — desligado (501)
		// quando a frescura está DORMENTE (Node.ChallengeIssuer nil). Ver handleChallenge.
		{"POST /runs/{id}/challenge", h.handleChallenge, planoControlo},
		{"POST /runs/{id}/resume", h.handleResume, planoControlo},
		// DECISÃO sobre um PROMPT DE EXAUSTÃO de orçamento (AOS-263, parte 3). MESMA admissão do
		// /approve e do /pause e a MESMA autenticação non-signing do canal de controlo — o
		// Ed25519Authenticator composto de AOS_OPERATORS, com nonce DURÁVEL de uso-único e
		// frescura. A assinatura cobre a DECISÃO e a PERGUNTA (kind próprio + payload canónico),
		// pelo que um pause capturado não se converte num abort. Ver exhaustion_decision.go.
		{"POST /runs/{id}/exhaustion", h.handleExhaustionDecision, planoControlo},
		// AUTONOMIA (AOS-087) — mudar níveis passa a ser uma operação de governação assinada e
		// selada, em vez de uma edição de ficheiro no servidor seguida de reiniciar o nó.
		{"POST /autonomy", h.handleAutonomySet, planoControlo},
		{"GET /autonomy", h.handleAutonomyGet, planoControlo},
		{"POST /autonomy/simular", h.handleAutonomySimular, planoControlo},
		// RATIFICAÇÃO de auto-modificação (AOS-275, achado F7). NÃO é por-run: o alvo é um
		// ARTEFACTO (skill/memória procedural), pelo que a rota é de NÓ. A assinatura ed25519 do
		// ratificador vem no corpo e é o gate de PRODUÇÃO (freshness + nonce-store durável
		// forçados) que a verifica contra a pubkey PINADA. Ver promotion_api.go.
		{"POST /promote", h.handlePromote, planoControlo},

		// Plano de GOVERNANÇA — DSAR / crypto-shredding (AOS-172, Art. 17). Autenticado pela
		// credencial FORTE do gate soberano (readGov) no corpo + admission do plano de controlo;
		// desligado se o fluxo não estiver composto (fail-closed). Ver handleDSAR.
		//
		// Sobre a AUSÊNCIA do mTLS neste plano — e é uma decisão em aberto, não uma omissão — ver
		// o comentário de `barreirasDe` em planos.go.
		{"POST /dsar/erase", h.handleDSAR, planoGovernacao},
		// Plano de GOVERNANÇA — ADMINISTRAÇÃO de legal hold e expiração (AOS-213, CON-02/DEF-903).
		// Autenticadas pela MESMA credencial forte do /dsar/erase (readGov); desligadas (501) se o
		// legal hold / job de expiração ou o gate soberano não estiverem compostos (fail-closed).
		// Ver legalhold.go.
		{"POST /dsar/hold", h.handleHold, planoGovernacao},
		{"POST /dsar/release", h.handleRelease, planoGovernacao},
		{"POST /dsar/expire", h.handleExpire, planoGovernacao},
	}
}
