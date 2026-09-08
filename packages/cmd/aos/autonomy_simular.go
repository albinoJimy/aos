package main

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/aos-ref/control-plane/governance/autonomy"
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
)

// ------------------------------------------------------------------------------------------
// POST /autonomy/simular — o que uma configuração TERIA feito às tool calls já executadas.
//
// Sem isto, mudar de nível é um salto: liga-se e descobre-se. E descobrir em produção que a L4
// escala tudo é exactamente o cenário que este projecto passou um dia inteiro a evitar.
//
// A simulação NÃO inventa estado. Relê os selos de mediação do WORM — que já carregam capability,
// recurso, taint, reversibilidade e sensibilidade — e RE-CLASSIFICA com o MESMO classificador
// puro que o nó usa. A classe de risco não é lida de um campo gravado, é recalculada: um campo
// gravado poderia ter sido produzido por uma versão anterior do classificador, e a simulação
// passaria a prever o passado em vez do presente.
// ------------------------------------------------------------------------------------------

// simularRequest é a configuração HIPOTÉTICA a avaliar.
type simularRequest struct {
	// Levels são as regras propostas, no mesmo formato de AOS_AUTONOMY_LEVELS
	// ("agt-1:fs=L4,class:agent-worker:http=L3").
	Levels string `json:"levels"`
	// Default é o piso proposto ("L0".."L5"); vazio ⇒ L0.
	Default string `json:"default"`
	// Limite de selos a percorrer (0 ⇒ 200). Bounded de propósito: um WORM grande não pode
	// transformar um pedido de simulação num varrimento sem fim.
	Max int `json:"max"`
}

// classeNaoSeladaNoWORM é a CLASSE de agente com que a simulação resolve o nível: vazia, DE
// PROPÓSITO. O `audit.AuditRecord` selado NÃO transporta a classe do agente (nem pode, sem uma
// migração de SchemaVersion da hash-chain), pelo que a simulação não tem a classe que a produção
// (`pdp.applyAutonomy`) usa em `LevelForAgentOrClass(agent, in.Principal.AgentClass, domain)`. A
// constante nomeia essa ausência em vez de a esconder num literal `""` no meio da chamada: o vazio
// SALTA o degrau `class:` da cascata e cai directamente para instância → piso.
const classeNaoSeladaNoWORM = ""

type simularEfeito struct {
	Run        string `json:"run"`
	Step       string `json:"step"`
	Agent      string `json:"agent"`
	Capability string `json:"capability"`
	Domain     string `json:"domain"`
	RiskClass  string `json:"risk_class"`
	Level      string `json:"level"`
	Effect     string `json:"effect"` // "corre" | "escala"
	// ClasseModelada declara, por EFEITO, se a classe de agente entrou na resolução do nível. É
	// SEMPRE false: o selo WORM não carrega a classe (ver [classeNaoSeladaNoWORM]), pelo que cada
	// efeito é resolvido só por instância + piso. O campo existe para a resposta não MENTIR por
	// omissão — um consumidor que veja `class:` nas regras propostas e um nível resolvido saberia,
	// sem ele, presumir que a classe foi considerada.
	ClasseModelada bool `json:"classe_modelada"`
}

// handleAutonomySimular avalia a configuração proposta contra o histórico selado.
func (h *apiHandler) handleAutonomySimular(w http.ResponseWriter, r *http.Request) {
	// ADMISSÃO e mTLS do plano de controlo vêm da TABELA DE ROTAS (planoControlo, ver planos.go),
	// não do corpo. Foi precisamente aqui que a convenção falhou: estas três rotas nasceram sem as
	// duas barreiras enquanto o plano e o comentário afirmavam que passavam "pela mesma admissão do
	// /approve". Agora a classificação é obrigatória no registo e o valor-zero aborta o arranque.
	// AUTENTICACAO DE GOVERNACAO — a MESMA credencial forte do /dsar/erase e do legal hold.
	//
	// Esta rota devolve run ids, step ids, NHIs de agente, capabilities e dominios de recurso
	// lidos do WORM SELADO. E a mesma classe de dados que o read-path protege com 404 uniformes,
	// e a primeira versao desta rota nao verificava credencial NENHUMA.
	//
	// A classificacao `planoControlo` da-lhe o balde de admissao e o mTLS — mas o mTLS NAO esta
	// composto em producao e o edge encaminha `location /` para o no. Sem esta barreira, quem
	// alcancasse a porta lia um resumo do historico selado sem apresentar nada.
	//
	// Foi o defeito do dia repetido numa forma nova: a rota nasceu com a barreira de TRANSPORTE
	// classificada e sem a de IDENTIDADE, e nenhum teste perguntou por ela porque eu so tinha
	// pensado na primeira.
	if h.readGov == nil {
		writeError(w, http.StatusNotImplemented, "simulacao desligada (governanca soberana nao composta)")
		return
	}
	leitor, ok := h.readGov.authorize(r)
	if !ok {
		writeError(w, http.StatusForbidden, "nao autorizado")
		return
	}
	if h.node == nil || h.node.WORM == nil {
		writeError(w, http.StatusNotImplemented, "sem WORM composto — nao ha historico para simular")
		return
	}
	var req simularRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	max := req.Max
	if max <= 0 || max > 2000 {
		max = 200
	}

	// A configuração proposta passa pelo MESMO parser da de arranque. Se não passasse, a
	// simulação aceitaria configurações que o nó recusaria — e diria que estava tudo bem sobre
	// uma tabela que nunca chegaria a entrar em vigor.
	proposto, err := parseAutonomyLevelsFrom(req.Levels)
	if err != nil {
		writeError(w, http.StatusBadRequest, "levels invalidos")
		return
	}
	piso := autonomy.L0
	if s := strings.TrimSpace(req.Default); s != "" {
		lvl, perr := parseAutonomyLevel(s)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "default invalido (esperado L0..L5)")
			return
		}
		piso = lvl
	}
	// Registo EFÉMERO, sem sink: a simulação não sela nada. Uma simulação que escrevesse no
	// trilho de auditoria contaminaria o registo com decisões que ninguém tomou.
	hipotese := autonomy.NewLevelRegistry(autonomy.WithDefaultLevel(piso))
	for _, s := range proposto {
		if _, serr := hipotese.SetLevel(r.Context(), s.agent, s.domain, s.level, "simulacao", "simulacao"); serr != nil {
			writeError(w, http.StatusBadRequest, "levels invalidos")
			return
		}
	}

	registos := h.mediacoesVisiveisPara(r, leitor, max)
	efeitos := make([]simularEfeito, 0, len(registos))
	var correm, escalam int
	for _, rec := range registos {
		classe := reclassificar(rec)
		dominio := autonomy.DomainOf(rec.Capability, rec.Resource.Value)
		// A classe do agente NÃO está no selo WORM ([classeNaoSeladaNoWORM]): resolve-se por
		// instância + piso, saltando o degrau `class:` da cascata. É declarado no efeito
		// (`classe_modelada:false`) e, se as regras propostas tiverem regras de classe, também no
		// topo da resposta — a simulação não finge modelar o que não tem.
		nivel := hipotese.LevelForAgentOrClass(rec.Principal.NHIID, classeNaoSeladaNoWORM, dominio)
		modo := autonomy.Oversight(nivel, classe)
		efeito := "corre"
		if modo.RequiresHumanGate() {
			efeito = "escala"
			escalam++
		} else {
			correm++
		}
		efeitos = append(efeitos, simularEfeito{
			Run: rec.RunID, Step: rec.StepID, Agent: rec.Principal.NHIID,
			Capability: rec.Capability, Domain: dominio,
			RiskClass: classe.String(), Level: nivel.String(), Effect: efeito,
			ClasseModelada: false,
		})
	}

	resposta := map[string]any{
		"avaliados":  len(efeitos),
		"correriam":  correm,
		"escalariam": escalam,
		"efeitos":    efeitos,
		// LIMITE DECLARADO, e não em rodapé: a simulação avalia SÓ o overlay de autonomia. Uma
		// tool call pode ser negada pelo escopo, pelo taint, pelo orçamento ou pelo egress muito
		// depois de a autonomia a deixar correr. "corre" aqui significa "a autonomia não a
		// escala", nunca "vai ser executada".
		"nota": "avalia SO o overlay de autonomia; escopo, taint, orcamento e egress decidem depois e podem negar",
	}

	// LIMITAÇÃO DE CLASSE (AOS-377). Se a configuração proposta tiver QUALQUER regra `class:`, a
	// simulação é KNOWN-INCOMPLETE: os selos WORM não carregam a classe do agente
	// ([classeNaoSeladaNoWORM]), pelo que uma regra de classe que em produção casaria com estes
	// registos NÃO é modelada aqui — os resultados reflectem só a resolução por instância + piso. É
	// declarado no TOPO, e não só por efeito, porque é uma limitação da simulação INTEIRA, não de um
	// registo: sem esta linha, um operador que propusesse `class:agent-worker:fs=L4` veria "escalariam
	// 0" e concluiria que a regra não muda nada, quando o que acontece é que ela nem foi avaliada.
	if propostoTemRegraDeClasse(proposto) {
		resposta["limitacao"] = "regras `class:` propostas NAO sao modeladas: os selos WORM nao carregam a classe do agente (seria uma migracao de SchemaVersion da hash-chain), pelo que a resolucao aqui usa so instancia + piso. Cada efeito declara-o em classe_modelada:false. Em producao, pdp.applyAutonomy resolve pela classe REAL (Principal.AgentClass), pelo que uma regra de classe pode mudar o resultado de forma que esta simulacao NAO preve."
	}

	writeJSON(w, http.StatusOK, resposta)
}

// propostoTemRegraDeClasse diz se alguma regra proposta tem alvo de CLASSE (prefixo
// [autonomy.ClassPrefix]). É o gatilho da declaração de limitação: uma regra de classe é
// exactamente a que a simulação não consegue modelar sobre selos que não carregam a classe.
func propostoTemRegraDeClasse(specs []autonomyLevelSpec) bool {
	for _, s := range specs {
		if strings.HasPrefix(s.agent, autonomy.ClassPrefix) {
			return true
		}
	}
	return false
}

// lerMediacoes recolhe os selos de TOOL CALL mais recentes do WORM.
//
// Bounded por construção. Ignora as partições de governação (gov.read, governance.*, autonomy):
// não são mediações de tool call, e incluí-las inflaria a contagem com registos que a autonomia
// nunca decidiu.
func lerMediacoes(ctx context.Context, worm audit.Store, max int) []audit.AuditRecord {
	lister, ok := worm.(interface{ Partitions() []string })
	if !ok {
		return nil
	}
	parts := lister.Partitions()
	sort.Sort(sort.Reverse(sort.StringSlice(parts)))
	out := make([]audit.AuditRecord, 0, max)
	for _, p := range parts {
		if len(out) >= max {
			break
		}
		if strings.HasPrefix(p, "gov.") || strings.HasPrefix(p, "governance.") || p == "autonomy" {
			continue
		}
		head, err := worm.Head(ctx, p)
		if err != nil || head == 0 {
			continue
		}
		recs, err := worm.Read(ctx, p, 1, head)
		if err != nil {
			continue
		}
		for _, rec := range recs {
			if rec.ToolID == "" || rec.Capability == "" {
				continue
			}
			out = append(out, rec)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

// reclassificar recalcula a classe de risco a partir dos MESMOS factos que o selo guardou.
//
// Reconstrói-se um [rm.Call] e usa-se o classificador real, em vez de reimplementar as regras
// aqui. Uma segunda implementação divergiria da primeira, e a simulação passaria a prever um
// sistema que não é este — que é pior do que não simular, porque produz confiança em vez de a
// medir.
func reclassificar(rec audit.AuditRecord) risk.Class {
	call := &rm.Call{
		Capability: rec.Capability,
		Resource:   rm.Resource{Type: rec.Resource.Type, Value: rec.Resource.Value, Region: rec.Resource.Region},
	}
	call.Context.Taint = rec.Context.Taint
	call.Context.Reversibility = rec.Context.Reversibility
	call.Context.Sensitivity = rec.Context.Sensitivity
	c := rm.NewRiskClassifier(nil)
	if _, err := c.Evaluate(context.Background(), call); err != nil {
		return risk.ClassDanger // fail-closed: sem classificação, o pior caso
	}
	switch call.Context.RiskClass {
	case risk.ClassSafe.String():
		return risk.ClassSafe
	case risk.ClassGray.String():
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}

// mediacoesVisiveisPara recolhe as mediações que ESTE leitor pode ver, e não as do nó inteiro.
//
// A autenticação sozinha não chegava. `authorize` diz QUEM é o leitor e qual a sua região; não
// diz que ele pode ler um run concreto. Sem o filtro, um leitor de um board veria os run ids,
// NHIs e recursos de TODAS as regiões — a recusa cross-region (AOS-172/205) contornada por uma
// rota de conforto, que é a forma mais barata de perder a propriedade central deste sistema.
//
// A decisão por run vem de [readGovernance.podeLerRun], a MESMA função que o `GET /runs/{id}` usa
// para a fronteira de residência. Não se reimplementa a regra aqui: uma segunda implementação
// divergiria da primeira, e a que divergisse em silêncio seria esta — a que ninguém olha.
//
// O veredicto é MEMORIZADO por run: um lote de 200 mediações costuma cair sobre poucas dezenas de
// runs, e sem cache seria uma consulta à autoridade por registo.
func (h *apiHandler) mediacoesVisiveisPara(r *http.Request, leitor readerIdentity, max int) []audit.AuditRecord {
	if h.readGov == nil {
		return nil // fail-closed: sem gate composto não se devolve histórico.
	}
	todas := lerMediacoes(r.Context(), h.node.WORM, max)

	// A identidade JÁ foi verificada pelo handler. Pergunta-se por run com [podeLerRun], que
	// aplica a MESMA regra de residência do `GET /runs/{id}` — e NÃO se volta a verificar a
	// credencial.
	//
	// A primeira versão chamava a authz de leitura POR-RUN a cada run, e isso re-verificava o token a cada
	// vez. Com credencial OIDC — que é a de produção — o verificador tem anti-replay por `jti`:
	// a primeira verificação (a do handler) consome-o, e todas as seguintes devolvem
	// `ErrTokenReplayed`. A rota teria devolvido `avaliados: 0` SEMPRE, em silêncio. O teste não o
	// via porque compunha o gate pela via legada de headers, que nunca chama o verificador —
	// testei o caminho que produção não usa.
	pode := make(map[string]bool, 16)
	out := make([]audit.AuditRecord, 0, len(todas))
	for _, rec := range todas {
		v, visto := pode[rec.RunID]
		if !visto {
			_, v = h.readGov.podeLerRun(r.Context(), leitor, rec.RunID)
			pode[rec.RunID] = v
		}
		if v {
			out = append(out, rec)
		}
	}
	return out
}
