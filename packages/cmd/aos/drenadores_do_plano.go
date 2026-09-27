package main

// drenadores_do_plano.go — QUEM PODE DRENAR A FILA DE PLANOS É UMA LISTA FECHADA (AOS-439).
//
// # O QUE ESTAVA ABERTO
//
// `POST /plans/claim` e `POST /plans/outcome` só exigiam uma identidade AUTENTICADA da região do
// pedido (`readGov.authorize`). Qualquer principal com um ID-token do IdP de soberania — um
// utilizador humano que submete planos, um service account de leitura — reclamava pedidos ALHEIOS e
// recebia o objectivo DECIFRADO (o nó decifra-o server-side sob a KEK do titular antes de o
// entregar, AOS-429). E fechava-os com um desfecho `terminal` inventado.
//
// Isto era pré-condição do AOS-439: o vínculo reclamação→run (quem reclamou é quem pode submeter o
// run filho em nome do submissor) só vale se «quem reclamou» for alguém que o dono autorizou a
// drenar. Sem a lista, «a reclamação viva é do chamador» provava só que o chamador se tinha
// adiantado.
//
// # A FORMA
//
// `AOS_PLAN_DRAINERS` — os `sub` (OIDC) dos principals drenadores, separados por vírgula.
// FAIL-CLOSED: vazia ⇒ NINGUÉM reclama nem reporta, e o banner di-lo. Em produção o valor é o `sub`
// do service account `aos-reader`, que é quem o `aos-orq consume` usa (`deploy/server`).
//
// A lista é de NOMES EXACTOS, sem curingas: um `*` é recusado no arranque, e não interpretado.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrBadPlanDrainers — `AOS_PLAN_DRAINERS` malformada. Aborta o arranque: uma lista que o nó não
// sabe ler não pode ser lida como «ninguém» (esconderia o erro do operador) nem como «todos».
var ErrBadPlanDrainers = errors.New("aos: AOS_PLAN_DRAINERS invalida")

// parsePlanDrainers interpreta a lista de drenadores. Vazia ⇒ nil (ninguém drena).
//
// Recusa: entrada com espaço interno, curinga (`*`), prefixo `human:` (o que se compara é o `sub`
// do ID-token, sem prefixos) e repetições — pelas razões de [parseMandateSigners]: uma lista de
// autoridade com duplicados é uma lista que alguém escreveu à pressa.
func parsePlanDrainers(s string) ([]string, error) {
	var out []string
	visto := map[string]bool{}
	for _, bruto := range strings.Split(s, ",") {
		p := strings.TrimSpace(bruto)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, " \t\r\n") {
			return nil, fmt.Errorf("%w: entrada %q com espaco interno", ErrBadPlanDrainers, p)
		}
		if strings.Contains(p, "*") {
			return nil, fmt.Errorf("%w: entrada %q com curinga — a lista e de nomes exactos", ErrBadPlanDrainers, p)
		}
		if strings.HasPrefix(p, "human:") {
			return nil, fmt.Errorf("%w: entrada %q com o prefixo human: — usa o sub do ID-token", ErrBadPlanDrainers, p)
		}
		if visto[p] {
			return nil, fmt.Errorf("%w: entrada %q repetida", ErrBadPlanDrainers, p)
		}
		visto[p] = true
		out = append(out, p)
	}
	return out, nil
}

// conjuntoDeDrenadores projecta a lista validada no conjunto que as rotas consultam.
func conjuntoDeDrenadores(lista []string) (map[string]bool, error) {
	out := make(map[string]bool, len(lista))
	for _, p := range lista {
		p2 := strings.TrimSpace(p)
		if p2 == "" || p2 != p || strings.Contains(p, "*") {
			return nil, fmt.Errorf("%w: entrada %q invalida", ErrBadPlanDrainers, p)
		}
		if out[p] {
			return nil, fmt.Errorf("%w: entrada %q repetida", ErrBadPlanDrainers, p)
		}
		out[p] = true
	}
	return out, nil
}

// eDrenador diz se o principal AUTENTICADO pode drenar a fila. Nó sem lista ⇒ ninguém pode.
func (h *apiHandler) eDrenador(principal string) bool {
	if h == nil || h.node == nil || principal == "" {
		return false
	}
	return h.node.PlanDrainers[principal]
}

// planDrainersPostureBanner declara, no arranque, quem pode drenar a fila.
func planDrainersPostureBanner(drenadores map[string]bool) []string {
	if len(drenadores) == 0 {
		return []string{
			"drenadores da fila de planos (AOS-439): NENHUM — AOS_PLAN_DRAINERS vazia, e a lista e " +
				"FAIL-CLOSED: POST /plans/claim e POST /plans/outcome recusam (403) TODO o chamador, " +
				"e os pedidos ficam na fila sem ninguem que os corra. Para drenar: AOS_PLAN_DRAINERS=<sub " +
				"do service account do aos-orq consume>",
		}
	}
	nomes := make([]string, 0, len(drenadores))
	for p := range drenadores {
		nomes = append(nomes, strconv.Quote(p))
	}
	sort.Strings(nomes)
	return []string{
		"drenadores da fila de planos (AOS-439): " + strconv.Itoa(len(nomes)) + " principal(is) — " +
			strings.Join(nomes, ", ") + ". SO eles reclamam pedidos (e recebem o objectivo decifrado) e " +
			"reportam desfechos; qualquer outra identidade autenticada recebe 403. E so quem tem a " +
			"reclamacao VIVA de um pedido pode submeter o run filho em nome do submissor (plan_request " +
			"no POST /runs)",
	}
}
