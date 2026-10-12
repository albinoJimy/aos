package bancoensaio

import (
	"errors"
	"testing"
)

// OPENROUTER_MODELO aceita uma LISTA separada por vírgulas, como KIMI_MODELOS: o primeiro é o
// da omissão e `--modelo` escolhe outro DA LISTA. Um modelo fora da lista continua recusado —
// quem autoriza cada modelo é o dono, no ficheiro —, e cada nome da lista é validado.
func TestAOS523_OpenRouter_ListaDeModelos(t *testing.T) {
	com := func(lista string) *Chaves {
		t.Helper()
		campos := chavesComOpenRouter()
		campos[CampoOpenRouterModelo] = lista
		c, err := LerChaves(escreverChaves(t, campos))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	lista := "autor-a/modelo-um, autor-b/modelo-dois,autor-c/modelo-tres"
	for pedido, quer := range map[string]string{
		"":                    "autor-a/modelo-um",
		"autor-a/modelo-um":   "autor-a/modelo-um",
		"autor-b/modelo-dois": "autor-b/modelo-dois",
		"autor-c/modelo-tres": "autor-c/modelo-tres",
	} {
		r, err := com(lista).Rota(FornecedorOpenRouter, pedido, Destino{})
		if err != nil || r.Modelo != quer {
			t.Errorf("--modelo %q: modelo %q, erro %v; quer %q", pedido, r.Modelo, err, quer)
		}
	}
	// Fora da lista: recusado, com o campo nomeado.
	_, err := com(lista).Rota(FornecedorOpenRouter, "autor-d/modelo-que-o-dono-nao-pos", Destino{})
	var campo *ErrCampoDasChaves
	if !errors.As(err, &campo) || campo.Campo != CampoOpenRouterModelo || campo.Problema != problemaForaDaLista {
		t.Errorf("modelo fora da lista: erro %v; quer %s/%s", err, CampoOpenRouterModelo, problemaForaDaLista)
	}
	// Um nome mal formado na lista não passa por estar ao lado de um bom.
	for _, ma := range []string{"autor-a/modelo-um,modelo-sem-autor", "autor-a/modelo-um,openrouter/autor-b"} {
		if _, err := com(ma).Rota(FornecedorOpenRouter, ma[len("autor-a/modelo-um,"):], Destino{}); err == nil {
			t.Errorf("lista %q: o nome mal formado foi aceite", ma)
		}
	}
	// A forma de sempre — um só nome — continua a valer.
	if r, err := com(modeloOpenRouterDeTeste).Rota(FornecedorOpenRouter, "", Destino{}); err != nil || r.Modelo != modeloOpenRouterDeTeste {
		t.Errorf("um so nome: %q, %v", r.Modelo, err)
	}
}
