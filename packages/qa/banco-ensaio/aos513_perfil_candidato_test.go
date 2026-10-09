package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-513 — O BANCO ACEITA UM PERFIL DE ROTA CANDIDATO: um perfil que não existe na tabela do nó
// corre aqui antes de o dono o assinar, e o relatório leva o seu digest.

const perfilCandidatoDeTeste = `{"requested":"rota-de-ensaio","expected_model":"openai/modelo-de-ensaio","wire_class":"openai-chat-completions","capabilities":["tools"],` +
	`"params":{"thinking":{"type":"disabled"},"max_tokens":16000},"projection_version":"1.2.0","devolver":"nunca"}`

// O PERFIL CHEGA AO PEDIDO. Com o candidato, todos os pedidos da corrida levam os parâmetros que
// ele declara e o protocolo da versão que ele nomeia; sem ele, nenhum pedido os leva.
func TestAOS513_Banco_OPerfilCandidatoChegaAoPedido(t *testing.T) {
	perfil, err := LerPerfilCandidato([]byte(perfilCandidatoDeTeste))
	if err != nil {
		t.Fatal(err)
	}
	if _, existe := modelgateway.RouteProfileFor(AliasDaRota); existe {
		t.Fatalf("o alias da rota de ensaio passou a existir na tabela de perfis do no: o teste deixou de provar que o candidato nao precisa de la estar")
	}
	montar := func(p *modelgateway.RouteProfile) *ambienteFalso {
		b, err := CarregarBateria()
		if err != nil {
			t.Fatal(err)
		}
		falso := NovoProviderFalso(nil)
		falso.GuardarCorpos()
		srv := httptest.NewServer(falso)
		t.Cleanup(srv.Close)
		no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Perfil: p})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(no.Fechar)
		return &ambienteFalso{bateria: b, falso: falso, no: no}
	}
	plano, err := planoDasOpcoes(mustBateria(t), opcoes{experiencia: string(ExperienciaBateria), braco: string(BracoA), semente: 513})
	if err != nil {
		t.Fatal(err)
	}
	com, sem := montar(perfil), montar(nil)
	com.correr(t, plano, nil)
	sem.correr(t, plano, nil)
	if len(com.falso.Corpos()) == 0 || len(com.falso.Corpos()) != len(sem.falso.Corpos()) {
		t.Fatalf("as duas corridas fizeram %d e %d pedidos", len(com.falso.Corpos()), len(sem.falso.Corpos()))
	}
	for i, corpo := range com.falso.Corpos() {
		if !bytes.Contains(corpo, []byte(`"max_tokens":16000,"thinking":{"type":"disabled"}`)) {
			t.Fatalf("pedido %d sem os parametros do perfil candidato: %s", i, corpo)
		}
		if !bytes.Contains(corpo, []byte("function-calling interface")) {
			t.Fatalf("pedido %d nao foi na versao da projeccao que o perfil nomeia (1.2.0)", i)
		}
	}
	for i, corpo := range sem.falso.Corpos() {
		if bytes.Contains(corpo, []byte(`"thinking"`)) || bytes.Contains(corpo, []byte(`"max_tokens"`)) {
			t.Fatalf("pedido %d leva parametros sem perfil: %s", i, corpo)
		}
	}
	// Um perfil de OUTRA rota não serve o nó de ensaio.
	outra := strings.Replace(perfilCandidatoDeTeste, `"rota-de-ensaio"`, `"outra-rota"`, 1)
	if _, err := LerPerfilCandidato([]byte(outra)); !errors.Is(err, ErrPerfilDoEnsaio) {
		t.Fatalf("um perfil de outra rota tinha de ser recusado; veio %v", err)
	}
}

func mustBateria(t *testing.T) *Bateria {
	t.Helper()
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A LINHA DE COMANDOS: `--perfil` lê o candidato pela leitura fechada, o digest vai no relatório
// e muda o digest da configuração; um perfil com um parâmetro fora do conjunto recusa a corrida
// sem escrever relatório.
func TestAOS513_Banco_LinhaDeComandos(t *testing.T) {
	escrever := func(doc string) string {
		caminho := filepath.Join(t.TempDir(), "perfil.json")
		if err := os.WriteFile(caminho, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return caminho
	}
	relatorio := func(args ...string) map[string]any {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, append([]string{"falso", "--saida", saida}, args...)...)
		if e.codigo != SaidaOK {
			t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
		}
		achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
		cru, err := os.ReadFile(achados[0])
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		if err := json.Unmarshal(cru, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	perfil, err := LerPerfilCandidato([]byte(perfilCandidatoDeTeste))
	if err != nil {
		t.Fatal(err)
	}
	com := relatorio("--perfil", escrever(perfilCandidatoDeTeste))["digests"].(map[string]any)
	sem := relatorio()["digests"].(map[string]any)
	if com["perfil"] != perfil.Digest() {
		t.Fatalf("o relatorio nao leva o digest do perfil candidato: %v", com["perfil"])
	}
	if _, tem := sem["perfil"]; tem || com["configuracao"] == sem["configuracao"] {
		t.Fatalf("sem perfil o relatorio nao tem o campo, e o digest da configuracao tem de diferir: com=%v sem=%v", com, sem)
	}
	for nome, doc := range map[string]string{
		"parametro fora do conjunto": strings.Replace(perfilCandidatoDeTeste, `"max_tokens":16000`, `"temperature":0.1`, 1),
		"valor de tipo errado":       strings.Replace(perfilCandidatoDeTeste, `"max_tokens":16000`, `"max_tokens":"muitos"`, 1),
		"outra rota":                 strings.Replace(perfilCandidatoDeTeste, `"rota-de-ensaio"`, `"gpt-4o"`, 1),
	} {
		saida := t.TempDir()
		e := executar(t, Ambiente{}, "falso", "--saida", saida, "--perfil", escrever(doc))
		achados, _ := filepath.Glob(filepath.Join(saida, "*"))
		if e.codigo != SaidaUso || len(achados) != 0 {
			t.Fatalf("%s: tinha de recusar sem escrever nada; codigo %d, ficheiros %v", nome, e.codigo, achados)
		}
	}
	if e := executar(t, Ambiente{}, "falso", "--saida", t.TempDir(), "--perfil", filepath.Join(t.TempDir(), "nao-existe.json")); e.codigo != SaidaUso {
		t.Fatalf("ficheiro de perfil inexistente: codigo %d", e.codigo)
	}
}
