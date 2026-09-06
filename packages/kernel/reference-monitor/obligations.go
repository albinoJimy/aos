package referencemonitor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Tipos de obrigação que o PEP SABE cumprir (AOS-087, AC4). A decisão do PDP pode
// anexar obrigações (TTL, região, redação, audit) que o PEP CUMPRE ANTES de
// libertar o efeito. Uma obrigação de tipo FORA deste conjunto é
// não-satisfazível: o PEP não a sabe impor, logo nega fail-closed (nunca a ignora
// silenciosamente — uma obrigação não-cumprida não pode viajar com um permit).
//
// O enforcement é GENÉRICO sobre o tipo [Obligation] (não sobre o PDP concreto): o
// RM (kernel) não importa o PDP (control-plane). A política de referência (AOS-007)
// emite hoje redact_pii + audit; região/ttl ficam prontas para quando a política as
// emitir (o contrato C1 já as prevê).
const (
	// ObligationAudit — o efeito tem de ser auditado (nível em Params["level"]). É
	// SATISFEITA pelo audit-before-effect do RM (RecordMediation grava a mediação,
	// incluindo as obrigações, ANTES do dispatch); não transforma os args.
	ObligationAudit = "audit"
	// ObligationRedactPII — os campos nomeados ([Obligation.Fields]) não podem
	// chegar ao efeito em claro. O PEP redige-os nos args ANTES do dispatch.
	ObligationRedactPII = "redact_pii"
	// ObligationRegion — o efeito só pode ocorrer dentro da região exigida
	// (Params["region"]). Uma call cross-border é NEGADA antes do dispatch.
	ObligationRegion = "region"
	// ObligationTTL — o resultado/efeito tem um tempo-de-vida (Params["seconds"]).
	// O PEP PROPAGA-A na [Decision.Obligations] para o consumidor a impor (viaja
	// com a decisão); não transforma os args.
	ObligationTTL = "ttl"
	// ObligationAutonomy — o overlay de autonomia (AOS-087/ADR-013) diz QUE oversight
	// o par (agente, domínio) × classe de risco exige (Params["oversight"]:
	// run/sample/suggest/confirm/batch). O PEP impõe-a: um oversight que exige HUMANO só
	// liberta o efeito com prova de aprovação VERIFICADA nesta call; os restantes modos
	// não transformam nada (viajam para o audit).
	//
	// Antes de existir, esta obligation caía no default fail-closed: QUALQUER permit
	// vindo do oráculo de autonomia era negado a jusante por "obrigação desconhecida".
	// Nunca se notou porque o veredicto habitual do oráculo é `escalate`, que
	// curto-circuita a cadeia ANTES do enforcement — só ao fechar o ciclo de aprovação
	// (AOS-021), com um permit a chegar ao fim, é que o efeito ficou observável.
	ObligationAutonomy = "autonomy"
)

// ParamAutonomyRequiresHuman é a chave do VEREDICTO de oversight na
// [ObligationAutonomy]: "true" ⇒ o efeito só é libertado com prova de aprovação humana
// verificada nesta call.
//
// O PEP lê o VEREDICTO, não o nome do modo. Reinterpretar o nome exigiria uma segunda
// cópia da taxonomia de autonomia aqui no kernel — e uma cópia diverge: divergiu, e o
// modo `post_hoc_sample` (que CORRE) foi tratado como exigente, negando acções legítimas
// até isto ser observado numa corrida real. Quem sabe compor nível × classe é o PDP; o
// PEP impõe o que ele decidiu.
const ParamAutonomyRequiresHuman = "requires_human"

// paramOversight é a chave do NOME do modo. Só para a mensagem de negação (atribuição
// legível); nunca para decidir.
const paramOversight = "oversight"

// redactedMarker é o valor determinístico que substitui um campo redigido. Não
// revela o comprimento nem qualquer fragmento do valor original.
const redactedMarker = "[REDACTED]"

// enforceObligations CUMPRE as obrigações coletadas na cadeia ANTES de o efeito ser
// libertado (AOS-087, AC4). Dirigido pelo [Obligation.Type]:
//
//   - audit: satisfeita pelo audit-before-effect (nada a transformar aqui);
//   - region: uma call que viola a região exigida (cross-border) ⇒ deny;
//   - redact_pii: redige os campos nomeados nos args (call.Input) in-place;
//   - ttl: propagada na Decision ao consumidor (nada a transformar aqui);
//   - QUALQUER outro tipo: desconhecido/não-satisfazível ⇒ deny fail-closed.
//
// Devolve (reason, causa, ok=false) se alguma obrigação for violada ou não-satisfazível;
// nesse caso o chamador nega fail-closed e NÃO despacha. Muta call.Input quando a
// redação se aplica (o fingerprint do permit não depende do Input — ver call.go).
//
// A CAUSA é devolvida para poder ser SELADA no registo da negação (AOS-341). A recusa
// pára na primeira obrigação que falha, pelo que a causa é sempre uma e exactamente uma:
// não é «as obrigações da call», é «a obrigação que recusou». A distinção é o que torna
// selá-la compatível com a assimetria do ramo `HookDeny` — ali descartam-se obrigações da
// base que nada aplicaram; aqui sela-se a que negou. Em ok=true a causa é o valor-zero e
// não tem significado.
func enforceObligations(call *Call, obligations []Obligation) (string, Obligation, bool) {
	for _, ob := range obligations {
		switch ob.Type {
		case ObligationAudit:
			// Satisfeita pelo RecordMediation (audit-before-effect) do RM.
		case ObligationTTL:
			// Propagada na Decision.Obligations ao consumidor (viaja com a decisão).
		case ObligationRegion:
			if reason, ok := enforceRegion(call, ob); !ok {
				return reason, ob, false
			}
		case ObligationRedactPII:
			if reason, ok := enforceRedactPII(call, ob); !ok {
				return reason, ob, false
			}
		case ObligationAutonomy:
			if reason, ok := enforceAutonomy(call, ob); !ok {
				return reason, ob, false
			}
		default:
			// Fail-closed: uma obrigação que o PEP não sabe cumprir não liberta o efeito.
			return fmt.Sprintf("obrigacao %q desconhecida/nao-satisfazivel: efeito negado (fail-closed)", ob.Type), ob, false
		}
	}
	return "", Obligation{}, true
}

// enforceAutonomy impõe o oversight de autonomia no PEP (AOS-087, AC4). Um modo que
// exige humano no ciclo só liberta o efeito se ESTA call trouxer prova de aprovação
// VERIFICADA — o campo não-exportado que só o [ApprovalGate] escreve. Sem ela, deny.
//
// É defesa em profundidade DELIBERADA: o PDP já rebaixa para escalate quando não há
// gate humano cumprido, e aqui o PEP volta a exigi-lo, agora sobre a prova concreta
// anexada à call. Uma decisão que chegue com "confirm" e sem prova é, por definição,
// uma decisão a que faltou o gate — e o PEP não a liberta.
func enforceAutonomy(call *Call, ob Obligation) (string, bool) {
	mode := strings.TrimSpace(ob.Params[paramOversight])
	switch strings.TrimSpace(ob.Params[ParamAutonomyRequiresHuman]) {
	case "false":
		return "", true // corre (com ou sem amostragem post-hoc): nada a impor aqui
	case "true":
		if call.humanApproved == nil {
			return fmt.Sprintf("oversight de autonomia %q exige aval humano e a call nao traz aprovacao verificada", mode), false
		}
		return "", true
	default:
		// Veredicto ausente ou não reconhecido: o PEP não o adivinha a partir do nome do
		// modo — foi essa adivinhação que introduziu a divergência. Fail-closed, como
		// qualquer obrigação que não se sabe cumprir.
		return fmt.Sprintf("obligation de autonomia sem veredicto %q utilizavel (oversight %q): efeito negado (fail-closed)", ParamAutonomyRequiresHuman, mode), false
	}
}

// enforceRegion impõe a obrigação de soberania de dados: o recurso-alvo tem de
// estar na região exigida. Uma call cross-border é negada ANTES do dispatch — o
// PEP nunca despacha um efeito que viola uma obrigação de região. Fail-closed: uma
// obrigação de região sem região exigida, ou um recurso sem região resolvida, ou
// uma região diferente da exigida ⇒ deny.
func enforceRegion(call *Call, ob Obligation) (string, bool) {
	required := ""
	if ob.Params != nil {
		required = strings.TrimSpace(ob.Params["region"])
		if required == "" {
			required = strings.TrimSpace(ob.Params["allowed"])
		}
	}
	if required == "" {
		return "obrigacao de regiao sem regiao exigida: nao-satisfazivel (fail-closed)", false
	}
	actual := strings.TrimSpace(call.Resource.Region)
	if actual == "" {
		return fmt.Sprintf("obrigacao de regiao %q mas recurso sem regiao resolvida: cross-border negado (fail-closed)", required), false
	}
	if !strings.EqualFold(actual, required) {
		return fmt.Sprintf("efeito viola obrigacao de regiao: recurso em %q, exigido %q (cross-border negado)", actual, required), false
	}
	return "", true
}

// enforceRedactPII redige, nos args da call (call.Input), os campos nomeados pela
// obrigação, para que o efeito NÃO veja a PII em claro. É uma redação determinística
// dirigida pela obrigação: se o Input é um objecto JSON, os campos nomeados (em
// qualquer nível de aninhamento) são substituídos por [redactedMarker].
//
// Fail-closed (a garantia "o efeito não vê PII em claro" tem de ser assegurável):
//   - redact_pii sem campos ⇒ alvo de redação indeterminado ⇒ deny;
//   - Input não-JSON e não-vazio ⇒ não há como localizar/redigir os campos com
//     garantia ⇒ deny (um blob opaco pode conter a PII em claro).
//
// Input vazio não tem PII a redigir (satisfeita, sem transformação).
func enforceRedactPII(call *Call, ob Obligation) (string, bool) {
	fields := make(map[string]struct{}, len(ob.Fields))
	for _, f := range ob.Fields {
		if f = strings.TrimSpace(f); f != "" {
			fields[f] = struct{}{}
		}
	}
	if len(fields) == 0 {
		return "redact_pii sem campos: alvo de redacao indeterminado (fail-closed)", false
	}
	if len(call.Input) == 0 {
		return "", true // sem payload, sem PII a redigir
	}
	var v any
	if err := json.Unmarshal(call.Input, &v); err != nil {
		return "redact_pii sobre input nao-JSON: redacao nao-garantida (fail-closed)", false
	}
	redactValue(v, fields)
	out, err := json.Marshal(v)
	if err != nil {
		// Um valor que desserializou de JSON re-serializa sempre; defensivo.
		return fmt.Sprintf("re-serializar input redigido: %v (fail-closed)", err), false
	}
	call.Input = out
	return "", true
}

// redactValue percorre um valor JSON desserializado e substitui, in-place, o valor
// de qualquer chave em fields por [redactedMarker], recursivamente em objectos e
// arrays. Uma chave redigida NÃO é percorrida (o subvalor é descartado).
func redactValue(v any, fields map[string]struct{}) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if _, ok := fields[k]; ok {
				t[k] = redactedMarker
				continue
			}
			redactValue(t[k], fields)
		}
	case []any:
		for i := range t {
			redactValue(t[i], fields)
		}
	}
}
