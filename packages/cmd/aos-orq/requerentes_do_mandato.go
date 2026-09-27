package main

// requerentes_do_mandato.go — O DRENADOR NÃO PLANEIA POR QUEM O MANDATO NÃO NOMEIA (AOS-439,
// revisão adversarial).
//
// O nó recusa o `POST /runs` de um run cujo submissor não conste dos `requesters` do mandato — mas
// isso é DEPOIS de o `serve` decompor o objectivo, o que corre o modelo com o NHI do mandato por
// um submissor que o humano não autorizou. A reclamação passou a trazer o `requested_by` do pedido;
// aqui confronta-se com os `requesters` do mandato do drenador (`--mandate`, o `mandato.json` que o
// timer de cunhagem também lê) e, se não constar, o pedido fecha JÁ com a saída 11, sem `serve`.
//
// FAIL-CLOSED: com `--mandate` dado, um mandato que não se leia aborta a drenagem ANTES de reclamar
// — reclamar e não saber decidir gastava uma geração. Um mandato v1 (sem `requesters`) não compara:
// a sua aceitação é a janela de migração do nó. Sob um v2, um pedido sem `requested_by` também fecha
// ([requerenteForaDoMandato]). E o mandato do NHI em uso tem de ser o do `mandato.json`
// ([cruzarMandatoComONHI]), senão a drenagem aborta antes de reclamar.
//
// A assinatura do mandato NÃO se verifica aqui — é o nó que decide, contra a chave pinada. Este
// passo só evita trabalho que o nó vai recusar; um mandato adulterado neste ficheiro faz no máximo
// o drenador recusar mais cedo, ou deixar o nó recusar mais tarde.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	identity "github.com/aos-ref/platform/identity"
)

// lerMandatoDoDrenador lê o `mandato.json` (a forma que o `aos-issuer mandate-sign` escreve).
// Campos desconhecidos recusam — a mesma disciplina do `mint-mandated`.
func lerMandatoDoDrenador(caminho string) (identity.Mandate, error) {
	f, err := os.Open(caminho)
	if err != nil {
		return identity.Mandate{}, fmt.Errorf("mandato do drenador: %w", err)
	}
	defer func() { _ = f.Close() }()
	var sm identity.SignedMandate
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sm); err != nil {
		return identity.Mandate{}, fmt.Errorf("mandato do drenador %s ilegivel: %w", caminho, err)
	}
	if err := sm.Mandate.Validate(); err != nil {
		return identity.Mandate{}, fmt.Errorf("mandato do drenador %s: %w", caminho, err)
	}
	return sm.Mandate, nil
}

// requerenteForaDoMandato diz se o pedido reclamado tem de fechar já, sem `serve`: o mandato é v2
// e o submissor do pedido não está nos seus `requesters`.
//
// UM PEDIDO SEM `requested_by` SOB UM v2 TAMBÉM FECHA (2.ª ronda da revisão). O nó recusa o
// `POST /runs` de um run sem submissor sob um mandato v2 — com a 403 UNIFORME, porque sem vínculo
// não há código próprio —, e essa recusa é classificada como transitória: o pedido voltava à fila e
// era re-oferecido para sempre, planeado de novo a cada vez. Um nó desta release manda sempre o
// `requested_by` de um pedido submetido com gate soberano; a ausência é um pedido de um nó sem gate,
// que o v2 não pode servir.
func requerenteForaDoMandato(m *identity.Mandate, p pedidoReclamado) bool {
	if m == nil || m.Version() < 2 {
		return false
	}
	if p.RequestedBy == "" {
		return true
	}
	return !m.AdmitsRequester(p.RequestedBy)
}

// mandatoIDDoNHI lê o id do mandato embebido no NHI em uso (AOS-439, 2.ª ronda da revisão), SEM
// verificar a assinatura: é só um cruzamento de consistência com o `mandato.json` — quem decide é o
// nó, pelo token. `ok` é false quando o NHI não se lê como um JWS com mandato (ficheiro ilegível,
// NHI falso de teste, ou emissor MANUAL, que não embebe mandato): aí não há o que cruzar.
func mandatoIDDoNHI(caminho string) (id string, ok bool) {
	bruto, err := os.ReadFile(caminho)
	if err != nil {
		return "", false
	}
	partes := strings.Split(strings.TrimSpace(string(bruto)), ".")
	if len(partes) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		return "", false
	}
	var c struct {
		Mandate *struct {
			Mandate struct {
				ID string `json:"id"`
			} `json:"mandate"`
		} `json:"mandate"`
	}
	if json.Unmarshal(payload, &c) != nil || c.Mandate == nil || c.Mandate.Mandate.ID == "" {
		return "", false
	}
	return c.Mandate.Mandate.ID, true
}

// cruzarMandatoComONHI recusa a drenagem quando o NHI em uso foi cunhado sob um mandato DIFERENTE
// do `mandato.json` (2.ª ronda da revisão). O caso real é a janela entre re-assinar o mandato e o
// timer de cunhagem trocar o NHI: o `consume` confrontaria os pedidos com os `requesters` do mandato
// NOVO e o nó decidiria pelos do ANTIGO — pedidos fechados com 11 que o nó teria aceite, ou o
// contrário. Abortar antes de reclamar deixa os pedidos na fila para a drenagem seguinte, já com o
// NHI novo. Só cruza quando os DOIS ids se lêem; um NHI sem mandato legível não cruza (declarado:
// o emissor manual e os NHI de teste).
func cruzarMandatoComONHI(m identity.Mandate, credFile string) error {
	id, ok := mandatoIDDoNHI(credFile)
	if !ok || id == m.ID {
		return nil
	}
	return fmt.Errorf("o NHI em uso foi cunhado sob o mandato %q e o mandato do drenador e %q — espera-se que o timer de cunhagem troque o NHI; "+
		"nao se reclama nada ate os dois coincidirem", id, m.ID)
}
