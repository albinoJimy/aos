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

type simularEfeito struct {
	Run        string `json:"run"`
	Step       string `json:"step"`
	Agent      string `json:"agent"`
	Capability string `json:"capability"`
	Domain     string `json:"domain"`
	RiskClass  string `json:"risk_class"`
	Level      string `json:"level"`
	Effect     string `json:"effect"` // "corre" | "escala"
}

// handleAutonomySimular avalia a configuração proposta contra o histórico selado.
func (h *apiHandler) handleAutonomySimular(w http.ResponseWriter, r *http.Request) {
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

	registos := lerMediacoes(r.Context(), h.node.WORM, max)
	efeitos := make([]simularEfeito, 0, len(registos))
	var correm, escalam int
	for _, rec := range registos {
		classe := reclassificar(rec)
		dominio := autonomy.DomainOf(rec.Capability, rec.Resource.Value)
		nivel := hipotese.LevelForAgentOrClass(rec.Principal.NHIID, "", dominio)
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
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"avaliados":  len(efeitos),
		"correriam":  correm,
		"escalariam": escalam,
		"efeitos":    efeitos,
		// LIMITE DECLARADO, e não em rodapé: a simulação avalia SÓ o overlay de autonomia. Uma
		// tool call pode ser negada pelo escopo, pelo taint, pelo orçamento ou pelo egress muito
		// depois de a autonomia a deixar correr. "corre" aqui significa "a autonomia não a
		// escala", nunca "vai ser executada".
		"nota": "avalia SO o overlay de autonomia; escopo, taint, orcamento e egress decidem depois e podem negar",
	})
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
