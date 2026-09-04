package main

// AUTORIDADE SOBRE O NÍVEL DE AUTONOMIA (AOS-305).
//
// O DEFEITO QUE ISTO FECHA. `POST /autonomy` — a rota que decide QUANTA SUPERVISÃO HUMANA se
// aplica a um par (agente, domínio) — autenticava-se com a assinatura de UM operador qualquer de
// AOS_OPERATORS, sem papel, sem tecto e sem segunda assinatura. Medido na auditoria
// (`analises/10` §3.1): uma assinatura levou um par de L0 a L5 num salto, para uma classe com que
// o operador não tinha relação nenhuma; a mesma tool call, antes escalada com
// `denied_by=policy`, deixou de escalar. O contraste estava no mesmo binário: o `/approve` exige
// a capability `approve:<classe>` de vocabulário fechado e ABORTA o arranque se duas entradas
// partilharem pubkey — precisamente para que "duas pessoas" esteja ancorado em criptografia.
//
// O QUE PASSA A VALER, e porquê nesta forma:
//
//   - Uma capability PRÓPRIA, `autonomy:set`, distinta de steer/pause. Vive em
//     AOS_AUTONOMY_SETTERS — a lista dos emitterIDs de AOS_OPERATORS que a detêm — e não numa
//     extensão da gramática de AOS_OPERATORS, para não mexer no parser de um registo de chaves
//     por causa de UMA rota. Um operador fora da lista assina e é recusado, mesmo para L1.
//     Fail-closed: lista vazia ⇒ NENHUM operador muda níveis, e o banner di-lo.
//   - DUAS assinaturas de operadores DISTINTOS para toda a transição que ATRAVESSE O LIMIAR DO
//     GATE HUMANO — qualquer mudança PARA L4 ou L5 (incluindo a de um par nunca registado). É em
//     L4 que `danger` deixa de confirmar cada acção e em L5 que passa a amostragem post-hoc
//     ([autonomy.Oversight]); é aí que a supervisão humana se remove, e é aí que a decisão
//     tem de ser de duas pessoas. A distinção de pubkeys entre os dois emissores é garantida
//     por [parseOperators]/[Bootstrap] (pubkey partilhada aborta o arranque), pelo que
//     "dois emitterIDs" É "duas chaves".
//   - A decisão registada do dono que AOS-263 cita (`specs/EPIC-20`, 2026-08-12) recusa
//     «caminhos de decisão humana mais fracos que o four-eyes já entregue» como regressão de
//     postura. Esta rota era exactamente isso.
//
// O que NÃO muda: as transições que NÃO atravessam o limiar (L0..L3, e qualquer DESCIDA) continuam
// a exigir UMA assinatura — mas de um emissor com `autonomy:set`. Baixar a autonomia é a
// direcção segura; exigir-lhe duas pessoas tornaria a redução de privilégio mais cara do que a
// concessão, que é o incentivo errado.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aos-ref/control-plane/governance/autonomy"
)

// autonomySetCapability é a capability que um emissor de AOS_OPERATORS tem de deter para mudar
// níveis de autonomia. Nomeada no vocabulário `<domínio>:<acção>` das outras capabilities de
// governação (`approve:<classe>`, `ratify:production`, `control:<kind>`), e é a que fica no
// registo: o selo de controlo grava `control:autonomy` como capability exercida, e este é o
// direito que o autoriza.
const autonomySetCapability = "autonomy:set"

// ErrBadAutonomySetters — AOS_AUTONOMY_SETTERS nomeia um operador que NÃO consta de
// AOS_OPERATORS, ou está malformada. Fail-closed no arranque: uma capability atribuída a um
// emitterID sem pubkey seria um operador que "pode" e nunca autentica — o mesmo defeito de
// [ErrBadOperatorEntry], visto do lado da autoridade.
var ErrBadAutonomySetters = errors.New("aos: AOS_AUTONOMY_SETTERS invalida — lista de emitterIDs (separados por virgula) que constam de AOS_OPERATORS e detem a capability autonomy:set; um id ausente de AOS_OPERATORS, vazio ou duplicado aborta o arranque")

// parseAutonomySetters lê a lista `id1,id2` de emitterIDs com `autonomy:set`. Vazio ⇒ (nil, nil):
// nenhum operador muda níveis, declarado no banner. A verificação de que cada id CONSTA de
// AOS_OPERATORS é do [Bootstrap], que é onde as duas listas se encontram.
func parseAutonomySetters(s string) ([]string, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue // vírgula final ou dupla ⇒ ruído tolerável, não um typo.
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: emitterID %q duplicado", ErrBadAutonomySetters, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadAutonomySetters, s)
	}
	sort.Strings(out) // ordem determinista ⇒ banner e erros reproduzíveis.
	return out, nil
}

// autonomyDualControlRequired diz se uma mudança PARA `to` atravessa o limiar do gate humano e
// por isso exige duas assinaturas. É uma função do DESTINO e só dele: o que se protege é o
// estado em que a acção danger deixa de esperar por um humano (L4 confirma só danger; L5 nem
// isso), independentemente de onde se vem — um par nunca registado a nascer em L5 é a mesma
// remoção de supervisão que um L0 promovido.
func autonomyDualControlRequired(to autonomy.Level) bool {
	return to >= autonomy.L4
}

// autonomySettersBanner declara QUEM pode mudar níveis e com QUE cerimónia — uma postura que,
// calada, deixaria o operador supor que "assinar chega". Segue a regra de posture_banner.go:
// cada afirmação deriva do estado composto ([Node.AutonomySetters]), não da intenção.
//
// Só faz sentido quando o oráculo está composto (sem ele a rota responde 501 e a autoridade é
// moot) — o chamador decide se a chama.
func autonomySettersBanner(setters map[string]bool) []string {
	if len(setters) == 0 {
		return []string{
			"autoridade sobre a autonomia (AOS-305): NENHUM operador detem autonomy:set — POST /autonomy RECUSA toda a mudanca de nivel (403) ate AOS_AUTONOMY_SETTERS nomear emitterIDs de AOS_OPERATORS. Os niveis em vigor sao os de AOS_AUTONOMY_LEVELS e os reidratados do WORM (AOS-307), e mudam POR REINICIO: o ambiente pode sempre BAIXAR um nivel posto por operador (a de-escalada e a direccao segura), e so nao o pode SUBIR",
		}
	}
	ids := make([]string, 0, len(setters))
	for id := range setters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return []string{
		// A QUALIFICAÇÃO «POR POST /autonomy» NÃO É DECORATIVA (achado de revisão adversarial
		// R-02). Sem ela, esta linha afirmava sem reservas que L4/L5 exigem duas assinaturas — e o
		// nó de referência imprimia-a no MESMO arranque em que aplicava L4 a partir de
		// AOS_AUTONOMY_LEVELS, sem assinatura nenhuma. A cerimónia governa a ROTA; o
		// provisionamento por ambiente é outra fronteira de confiança (quem edita o deployment e
		// reinicia), e o banner tem de dizer qual é qual em vez de deixar o operador supor.
		fmt.Sprintf("autoridade sobre a autonomia (AOS-305): %d operador(es) com autonomy:set [%s]. POR POST /autonomy: mudar PARA L4 ou L5 (o limiar em que danger deixa de esperar por um humano) exige DUAS assinaturas de emissores DISTINTOS desta lista (co_emitter), as restantes transicoes exigem UMA, e um emissor de AOS_OPERATORS fora desta lista e recusado mesmo para L1. POR AOS_AUTONOMY_LEVELS (provisionamento no arranque): QUALQUER nivel, incluindo L4/L5, e aplicado SEM assinatura — a fronteira de confianca ai e quem edita o deployment e reinicia, nao esta lista",
			len(ids), strings.Join(ids, ",")),
	}
}
