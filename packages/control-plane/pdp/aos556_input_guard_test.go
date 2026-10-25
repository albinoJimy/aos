package pdp

import (
	"reflect"
	"strings"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// GUARD-TEST DA FORMA DO `Input` (AOS-556, ADR-044 §2.4).
//
// O [inputFromCall] é o ÚNICO fio entre o Reference Monitor e o PDP, e já perdeu um campo: o
// comentário dele registra que a classe de risco não era copiada, e que a consequência não foi um
// deny visível mas o colapso da taxonomia de autonomia L0–L5 em dois estados — um mecanismo
// graduado presente, documentado e semanticamente inerte. O precedente do lado do `Call` diz o
// mesmo com mais números: `TestAOS454_AuditoriaCampoACampoCallActivityCall` existe porque a MESMA
// tradução perdeu um campo quatro vezes.
//
// Não existia nada equivalente para o `Input` (zero reflexão nos testes deste pacote), e a fase R2
// acrescenta-lhe campos. Passa a existir.
//
// O QUE ESTE TESTE APANHA, e o que não apanha. Apanha o campo ESQUECIDO: um campo do `Input` que
// nenhuma linha do adaptador preenche fica no zero e avermelha. **Não** apanha o campo copiado da
// fonte errada — para isso é a tabela de verdade do [TestDecide_GoldenTruthTable], que decide sobre
// valores concretos. É a divisão deliberada: aqui mede-se a PROPAGAÇÃO, lá o SIGNIFICADO.

// camposSemFonteNoCall são os campos do [Input] que NÃO se preenchem de um campo exportado do
// [rm.Call], com a razão. Um campo novo que não conste daqui TEM de chegar não-zero.
var camposSemFonteNoCall = map[string]string{
	// Vem de `call.HumanApproval()`, um campo NÃO-EXPORTADO que só o [rm.ApprovalGate] escreve
	// depois de verificar a evidência (amarra à preview, uso único, TTL). Sentinelas por reflexão
	// não lhe chegam — e é exactamente essa inacessibilidade que o AOS-021 exige: nem o modelo nem
	// conteúdo untrusted o podem pôr a true.
	"Context.HumanGateSatisfied": "vem de um campo nao-exportado que so o ApprovalGate escreve",
}

func TestAOS556_FormaDoInputPropagaCampoACampo(t *testing.T) {
	var call rm.Call
	preencherCallComSentinelas(t, reflect.ValueOf(&call).Elem(), "")

	in := inputFromCall(&call)

	zeros := map[string]string{}
	camposZero(reflect.ValueOf(in), "", zeros)

	for caminho := range zeros {
		if razao, declarado := camposSemFonteNoCall[caminho]; declarado {
			delete(zeros, caminho)
			if razao == "" {
				t.Errorf("%s: declarado sem razao", caminho)
			}
		}
	}
	for caminho, tipo := range zeros {
		t.Errorf("Input.%s (%s) chegou ZERO com todos os campos exportados do Call preenchidos: ou o adaptador nao o copia, ou tem de constar de camposSemFonteNoCall com a razao", caminho, tipo)
	}

	// NÃO-VACUIDADE, nos dois sentidos. Sem isto um `preencherCallComSentinelas` que não
	// preenchesse nada, ou um `camposZero` que não visitasse nada, davam verde.
	if call.Capability == "" || call.Principal.NHIID == "" || call.Context.Taint == "" {
		t.Fatal("as sentinelas nao foram escritas no Call: este teste nao mediu nada")
	}
	var vazio Input
	zerosDoVazio := map[string]string{}
	camposZero(reflect.ValueOf(vazio), "", zerosDoVazio)
	if len(zerosDoVazio) <= len(camposSemFonteNoCall) {
		t.Fatalf("camposZero so encontrou %d campos zero num Input VAZIO: o varredor esta partido", len(zerosDoVazio))
	}
}

// AS DECLARAÇÕES DE EXCEPÇÃO NÃO PODEM ENVELHECER. Um caminho declarado em
// [camposSemFonteNoCall] que deixe de existir no [Input] — renomeado, removido, ou passado a ser
// copiado — fica órfão, e uma declaração órfã é pior do que nenhuma: diz a quem lê que há uma
// excepção onde já não há. É a mesma regra das baselines dos gates, que só encolhem.
func TestAOS556_DeclaracoesDeExcepcaoDoInputNaoEnvelhecem(t *testing.T) {
	existentes := map[string]string{}
	todosOsCampos(reflect.TypeOf(Input{}), "", existentes)

	for caminho := range camposSemFonteNoCall {
		if _, ok := existentes[caminho]; !ok {
			t.Errorf("camposSemFonteNoCall declara %q, que nao e um campo do Input: remove a entrada", caminho)
		}
	}
}

// preencherCallComSentinelas põe um valor não-zero em cada campo EXPORTADO, recursivamente. Um
// kind sem caso declarado FALHA o teste em vez de ser saltado em silêncio: um campo novo de tipo
// novo que ficasse por preencher tornava este guarda vacuoso para ele.
func preencherCallComSentinelas(t *testing.T, v reflect.Value, caminho string) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		nome := f.Name
		if caminho != "" {
			nome = caminho + "." + f.Name
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("s-" + nome)
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Int, reflect.Int64:
			fv.SetInt(int64(1000 + i))
		case reflect.Slice:
			switch fv.Type().Elem().Kind() {
			case reflect.Uint8:
				fv.SetBytes([]byte("b-" + nome))
			case reflect.String:
				fv.Set(reflect.ValueOf([]string{"cap:" + nome}))
			case reflect.Struct:
				s := reflect.MakeSlice(fv.Type(), 1, 1)
				preencherCallComSentinelas(t, s.Index(0), nome+"[0]")
				fv.Set(s)
			default:
				t.Fatalf("slice de %s em %s sem sentinela declarada", fv.Type().Elem().Kind(), nome)
			}
		case reflect.Map:
			if fv.Type().Key().Kind() == reflect.String && fv.Type().Elem().Kind() == reflect.Slice {
				fv.Set(reflect.ValueOf(map[string][]string{"s-" + nome: {"cap:" + nome}}))
				continue
			}
			t.Fatalf("map %s em %s sem sentinela declarada", fv.Type(), nome)
		case reflect.Struct:
			preencherCallComSentinelas(t, fv, nome)
		case reflect.Ptr:
			continue // ponteiros do Call são evidência verificada, não entrada do chamador
		default:
			t.Fatalf("campo %s de kind %s sem sentinela declarada", nome, fv.Kind())
		}
	}
}

// camposZero recolhe, por caminho, os campos exportados de v que estão no valor zero.
func camposZero(v reflect.Value, caminho string, out map[string]string) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		nome := f.Name
		if caminho != "" {
			nome = caminho + "." + f.Name
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			camposZero(fv, nome, out)
			continue
		}
		if fv.IsZero() {
			out[nome] = fv.Kind().String()
		}
	}
}

// todosOsCampos recolhe os caminhos de todos os campos exportados FOLHA de t.
func todosOsCampos(t reflect.Type, caminho string, out map[string]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		nome := f.Name
		if caminho != "" {
			nome = caminho + "." + f.Name
		}
		if f.Type.Kind() == reflect.Struct && !strings.HasPrefix(f.Type.String(), "time.") {
			todosOsCampos(f.Type, nome, out)
			continue
		}
		out[nome] = f.Type.String()
	}
}
