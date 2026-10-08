package bancoensaio

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

// relogioFixo é o relógio dos testes: um instante fixo, em UTC.
func relogioFixo() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) }

// ambienteFalso é um nó de ensaio ligado a um provider falso em processo.
type ambienteFalso struct {
	bateria *Bateria
	falso   *ProviderFalso
	no      *NoDeEnsaio
}

// novoAmbienteFalso monta o nó de ensaio contra um provider falso com o roteiro dado.
func novoAmbienteFalso(t *testing.T, roteiro []Comportamento, contador *Contador) *ambienteFalso {
	t.Helper()
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	falso := NovoProviderFalso(roteiro)
	falso.GuardarCorpos()
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{
		Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Contador: contador,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	return &ambienteFalso{bateria: b, falso: falso, no: no}
}

// correr corre um plano no ambiente e devolve o relatório.
func (a *ambienteFalso) correr(t *testing.T, p Plano, contador *Contador) *Relatorio {
	t.Helper()
	r, err := Correr(context.Background(), CfgDaCorrida{
		Modo: ModoFalso, Plano: p, Bateria: a.bateria, No: a.no, Contador: contador,
		Rota:    RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
		Relogio: relogioFixo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
