package pdp

import (
	"context"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// INTEGRAÇÃO AOS-089 — o PDP consulta a taxonomia de autonomia L0–L5 no caminho de
// decisão e sobrepõe o OVERSIGHT PROPORCIONAL (nível × classe de risco) a uma
// decisão de base permit.
//
// CAMADAS / INVERSÃO DE DEPENDÊNCIA. O nível vem de um [autonomy.Oracle] injectado
// (o registo concreto vive em control-plane/governance/autonomy — mesmo layer,
// sem ciclo: o autonomy NÃO importa o pdp). O PDP compõe o oversight com a função
// PURA [autonomy.Oversight] e traduz o modo resultante no efeito/obligation da
// Decision C1. A composição só TIGHTENS: um deny de base nunca é afrouxado, e um
// permit de base só pode ser escalado (defence-in-depth).

// WithAutonomyOracle liga o oráculo de níveis de autonomia (AOS-089) que o PDP
// consulta em CADA decisão para obter o nível corrente do par (agente, domínio) e
// compor o grau de oversight. Sem esta opção o overlay é inerte e o PDP decide como
// antes (comportamento idêntico — todos os testes de base mantêm-se). Um oráculo
// nil deixa o overlay inerte.
func WithAutonomyOracle(o autonomy.Oracle) Option {
	return func(p *PDP) { p.autonomyOracle = o }
}

// applyAutonomy sobrepõe o oversight de autonomia (nível × classe de risco) a uma
// decisão de BASE. É NO-OP se não houver oráculo ligado ou se a base não for permit
// (a autonomia só acrescenta oversight — nunca converte deny em permit).
//
// Para uma base permit: deriva o domínio da capability ([autonomy.DomainOf]),
// consulta o nível corrente (fail-closed [autonomy.L0] se o par não tiver registo),
// compõe o modo com [autonomy.Oversight] e:
//   - anexa uma obligation "autonomy" com o nível/domínio/modo/classe (auditável — o
//     RM sela-a na hash-chain e o PEP sabe COMO aplicar o gate);
//   - se o modo BLOQUEIA à espera de um humano (suggest/confirm/batch), rebaixa o
//     efeito para [Escalate] (requer gate humano — ADR-013); se corre (run) ou é
//     amostrado post-hoc, mantém [Permit];
//   - expõe o nível corrente num span aos.autonomy.level (AC4/DoD).
func (p *PDP) applyAutonomy(ctx context.Context, in Input, base Decision) Decision {
	if p.autonomyOracle == nil || base.Effect != Permit {
		return base
	}

	agent := in.Principal.ID
	domain := autonomy.DomainOf(in.Capability, in.Resource.Value)
	level := p.autonomyOracle.LevelFor(agent, domain)
	class := riskClassFromString(in.Context.RiskClass)
	mode := autonomy.Oversight(level, class)

	// Exposição do nível corrente na observabilidade (AC4/DoD). ExposeLevel trata um
	// tracer nil como Noop; anota-se a composição nível × classe no mesmo span.
	_, span := autonomy.ExposeLevel(ctx, p.tracer, agent, domain, level)
	autonomy.AnnotateOversight(span, class, mode)
	span.SetAttribute(attrAutonomyEffectBase, string(base.Effect))

	out := base
	// Obligation de autonomia (cópia defensiva das obligations de base).
	ob := Obligation{
		Type: obligationAutonomy,
		Params: map[string]string{
			"level":      level.String(),
			"domain":     domain,
			"oversight":  mode.String(),
			"risk_class": class.String(),
		},
	}
	out.Obligations = append(append([]Obligation(nil), base.Obligations...), ob)

	if mode.RequiresHumanGate() {
		out.Effect = Escalate
		out.Reason = "autonomia " + level.String() + " x " + class.String() + " -> " + mode.String() + " (gate humano)"
	} else {
		out.Reason = base.Reason + " | autonomia " + level.String() + " x " + class.String() + " -> " + mode.String()
	}
	span.SetAttribute(attrAutonomyEffectFinal, string(out.Effect))
	span.End()
	return out
}

// obligationAutonomy é o Type da obligation que transporta a decisão de oversight
// de autonomia para o PEP/audit (nível/domínio/modo/classe).
const obligationAutonomy = "autonomy"

// Atributos do span de decisão anotados pelo overlay de autonomia (além dos de
// [autonomy.ExposeLevel]): o efeito de base e o efeito final, para tornar visível
// o TIGHTEN permit→escalate.
const (
	attrAutonomyEffectBase  = "aos.autonomy.effect_base"
	attrAutonomyEffectFinal = "aos.autonomy.effect_final"
)

// riskClassFromString mapeia a classe de risco textual do [Input] para [risk.Class].
// FAIL-CLOSED: uma string vazia ou desconhecida resolve para [risk.ClassDanger] (o
// pior caso), garantindo o oversight máximo quando a classe não foi transportada.
func riskClassFromString(s string) risk.Class {
	switch s {
	case "safe":
		return risk.ClassSafe
	case "gray":
		return risk.ClassGray
	default: // "danger", "" e qualquer valor desconhecido
		return risk.ClassDanger
	}
}
