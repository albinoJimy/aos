package bancoensaio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AOS-518 — O DIGEST DO PRÓPRIO RELATÓRIO, nas duas direcções: recalculá-lo sobre o ficheiro dá o
// valor que o ficheiro traz; mudar qualquer contagem, uma razão ou o veredicto dá outro, e a
// verificação apanha-o.

// aos518RelatorioEscrito corre uma qualificação contra o falso bom e devolve os bytes do ficheiro.
func aos518RelatorioEscrito(t *testing.T) []byte {
	t.Helper()
	e, _ := aos518Qualificar(t, aos518PerfilSimples, "--roteiro", "cumpre", "--silencioso")
	achados, _ := filepath.Glob(filepath.Join(e.saida, "*.json"))
	if len(achados) != 1 {
		t.Fatalf("relatorios: %v", achados)
	}
	cru, err := os.ReadFile(achados[0])
	if err != nil {
		t.Fatal(err)
	}
	return cru
}

func TestAOS518_Digest_RecalculadoSobreOFicheiroEOMesmo(t *testing.T) {
	cru := aos518RelatorioEscrito(t)
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(r.DigestDoRelatorio) {
		t.Fatalf("digest do relatorio = %q", r.DigestDoRelatorio)
	}
	verificado, err := VerificarDigestDoRelatorio(cru)
	if err != nil || verificado != r.DigestDoRelatorio {
		t.Fatalf("a verificacao do ficheiro tal como foi escrito falhou: %q, %v", verificado, err)
	}
	if calculado, err := DigestDoRelatorio(cru); err != nil || calculado != r.DigestDoRelatorio {
		t.Fatalf("recalculado sobre o ficheiro = %q (%v), o ficheiro traz %q", calculado, err, r.DigestDoRelatorio)
	}
	// O DIGEST E O SHA256 DA FORMA CANONICA, e a forma canonica nao tem o campo do digest.
	canonico, err := FormaCanonicaDoRelatorio(cru)
	if err != nil {
		t.Fatal(err)
	}
	soma := sha256.Sum256(canonico)
	if "sha256:"+hex.EncodeToString(soma[:]) != r.DigestDoRelatorio {
		t.Errorf("o digest nao e o sha256 da forma canonica")
	}
	if bytes.Contains(canonico, []byte(CampoDoDigestDoRelatorio)) || bytes.ContainsAny(canonico, "\n") || !bytes.HasPrefix(canonico, []byte(`{"custo":`)) {
		t.Errorf("forma canonica mal formada: comeca por %.60s", canonico)
	}
	// O digest tambem vai no resumo, e o resumo diz que nao e coberto por ele.
	if resumo := ResumoEmTexto(&r); !strings.Contains(resumo, r.DigestDoRelatorio) {
		t.Errorf("o resumo nao cita o digest do relatorio")
	}

	// A FORMA DO FICHEIRO NAO CONTA: outra indentacao e outra ordem dos membros dao o mesmo digest.
	var generico map[string]any
	dec := json.NewDecoder(bytes.NewReader(cru))
	dec.UseNumber()
	if err := dec.Decode(&generico); err != nil {
		t.Fatal(err)
	}
	compacto, _ := json.Marshal(generico) // membros por ordem alfabetica, sem espacos
	if d, err := VerificarDigestDoRelatorio(compacto); err != nil || d != r.DigestDoRelatorio {
		t.Errorf("o mesmo documento, compacto e reordenado, deu %q (%v)", d, err)
	}
	if d, err := VerificarDigestDoRelatorio(bytes.ReplaceAll(cru, []byte("\n"), []byte("\r\n"))); err != nil || d != r.DigestDoRelatorio {
		t.Errorf("o mesmo documento com fins de linha do Windows deu %q (%v)", d, err)
	}
}

// aos518Folhas devolve o caminho de todas as folhas (numeros, textos, booleanos, nulos) de um documento.
func aos518Folhas(v any, caminho []any, out *[][]any) {
	switch x := v.(type) {
	case map[string]any:
		for k, f := range x {
			aos518Folhas(f, append(append([]any(nil), caminho...), k), out)
		}
	case []any:
		for i, f := range x {
			aos518Folhas(f, append(append([]any(nil), caminho...), i), out)
		}
	default:
		*out = append(*out, caminho)
	}
}

// aos518NovaFolha devolve um valor diferente do dado, do mesmo tipo quando o tem.
func aos518NovaFolha(v any) any {
	switch x := v.(type) {
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			return json.Number(x.String() + "1")
		}
		var n int64
		_, _ = fmt.Sscan(x.String(), &n)
		return json.Number(fmt.Sprint(n + 1))
	case string:
		return x + "x"
	case bool:
		return !x
	}
	return "deixou_de_ser_nulo"
}

// aos518Trocar muda, no documento, a folha do caminho dado, e devolve como a repor.
func aos518Trocar(t *testing.T, doc any, caminho []any) (repor func()) {
	t.Helper()
	v := doc
	for _, passo := range caminho[:len(caminho)-1] {
		switch x := v.(type) {
		case map[string]any:
			v = x[passo.(string)]
		case []any:
			v = x[passo.(int)]
		}
	}
	switch x := v.(type) {
	case map[string]any:
		k := caminho[len(caminho)-1].(string)
		antes := x[k]
		x[k] = aos518NovaFolha(antes)
		return func() { x[k] = antes }
	case []any:
		k := caminho[len(caminho)-1].(int)
		antes := x[k]
		x[k] = aos518NovaFolha(antes)
		return func() { x[k] = antes }
	}
	t.Fatalf("caminho invalido: %v", caminho)
	return nil
}

// MUDAR QUALQUER VALOR — cada contagem, cada razao, o veredicto, cada digest — da outro digest, e
// a verificacao recusa o ficheiro. Percorre TODAS as folhas do relatorio, uma a uma.
func TestAOS518_Digest_MudarQualquerValorMudaODigestEAVerificacaoApanha(t *testing.T) {
	cru := aos518RelatorioEscrito(t)
	original, err := VerificarDigestDoRelatorio(cru)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(cru))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	var folhas [][]any
	aos518Folhas(doc, nil, &folhas)
	if len(folhas) < 2000 {
		t.Fatalf("so %d folhas: o teste nao esta a percorrer o relatorio", len(folhas))
	}
	contagens, limiares, mudadas, dasObservacoes := 0, 0, 0, 0
	for _, caminho := range folhas {
		nome := fmt.Sprint(caminho...)
		if len(caminho) == 1 && caminho[0] == CampoDoDigestDoRelatorio {
			continue // o proprio campo: tratado abaixo
		}
		if caminho[0] == "observacoes" {
			// As linhas por run sao tres quartos das folhas e todas da mesma forma: muda-se uma
			// em cada dez. Tudo o resto — as contagens, as taxas, os digests — muda-se inteiro.
			if dasObservacoes++; dasObservacoes%10 != 0 {
				continue
			}
		}
		repor := aos518Trocar(t, doc, caminho)
		alterado, merr := json.Marshal(doc)
		repor()
		if merr != nil {
			t.Fatalf("%s: %v", nome, merr)
		}
		// A verificacao recalcula o digest do conteudo e compara-o com o que o ficheiro traz: se
		// a mudanca nao mudasse o digest, ela aceitava.
		if _, verr := VerificarDigestDoRelatorio(alterado); !errors.Is(verr, ErrDigestDoRelatorio) {
			t.Errorf("mudar %s passou na verificacao: o digest nao mudou", nome)
		}
		mudadas++
		if len(caminho) >= 2 && caminho[0] == "qualificacao_do_modelo" {
			switch caminho[1] {
			case "contagens":
				contagens++
			case "limiares":
				limiares++
			}
		}
	}
	// Reposto tudo, o documento volta a verificar: as mudancas nao ficaram.
	if reposto, _ := json.Marshal(doc); true {
		if d, verr := VerificarDigestDoRelatorio(reposto); verr != nil || d != original {
			t.Fatalf("depois de repor todas as folhas o documento ja nao verifica: %v", verr)
		}
	}
	// As 21 contagens de que o veredicto e funcao (os quatro mapas estao vazios numa corrida sem
	// falhas) e as 6 linhas de limiares foram todas mudadas.
	if contagens != 21 || limiares < 6*8 {
		t.Errorf("folhas mudadas: %d nas contagens (quer 21), %d nos limiares", contagens, limiares)
	}
	if mudadas < 500 {
		t.Errorf("so %d folhas mudadas", mudadas)
	}
	t.Logf("folhas mudadas uma a uma: %d de %d (das quais %d nas contagens e %d nos limiares da qualificacao); todas mudaram o digest", mudadas, len(folhas), contagens, limiares)

	// O digest calculado tambem muda (a outra funcao publica), numa contagem e no veredicto.
	for _, troca := range [][2]string{
		{`"veredicto": "qualificado"`, `"veredicto": "recusado"`},
		{`"razoes": []`, `"razoes": ["amostra_incompleta"]`},
		{`"nos_com_tools_cumpridos": 40`, `"nos_com_tools_cumpridos": 39`},
		{`"violado": false`, `"violado": true`},
	} {
		if !bytes.Contains(cru, []byte(troca[0])) {
			t.Fatalf("o relatorio nao tem %s", troca[0])
		}
		trocado := bytes.Replace(cru, []byte(troca[0]), []byte(troca[1]), 1)
		if novo, derr := DigestDoRelatorio(trocado); derr != nil || novo == original {
			t.Errorf("trocar %s por %s nao mudou o digest calculado (%v)", troca[0], troca[1], derr)
		}
		if _, verr := VerificarDigestDoRelatorio(trocado); !errors.Is(verr, ErrDigestDoRelatorio) {
			t.Errorf("trocar %s por %s passou na verificacao", troca[0], troca[1])
		}
	}
	// Acrescentar um membro tambem muda.
	if _, verr := VerificarDigestDoRelatorio(bytes.Replace(cru, []byte(`"modo":`), []byte(`"membro_a_mais": 1, "modo":`), 1)); !errors.Is(verr, ErrDigestDoRelatorio) {
		t.Errorf("um membro a mais passou na verificacao")
	}
	// Mudar o proprio digest para outro valor bem formado.
	outro := bytes.Replace(cru, []byte(original), []byte("sha256:"+strings.Repeat("0", 64)), 1)
	if _, verr := VerificarDigestDoRelatorio(outro); !errors.Is(verr, ErrDigestDoRelatorio) {
		t.Errorf("um digest que nao e o do conteudo passou na verificacao")
	}
}

// O que a verificacao recusa por forma: sem o campo, com membros repetidos, e o que nao e um
// objecto JSON. Um membro repetido e a maneira de um ficheiro dizer duas coisas a dois leitores.
func TestAOS518_Digest_Recusas(t *testing.T) {
	cru := aos518RelatorioEscrito(t)
	digest, _ := VerificarDigestDoRelatorio(cru)
	semCampo := regexp.MustCompile(`,\s*"`+CampoDoDigestDoRelatorio+`": "[^"]*"`).ReplaceAll(cru, nil)
	if bytes.Contains(semCampo, []byte(CampoDoDigestDoRelatorio)) {
		t.Fatal("o campo do digest nao foi retirado do ficheiro de teste")
	}
	// Sem o campo o digest calcula-se (e e o mesmo), mas a VERIFICACAO recusa: nao ha o que conferir.
	if d, err := DigestDoRelatorio(semCampo); err != nil || d != digest {
		t.Errorf("sem o campo, o digest calculado = %q (%v); quer o mesmo %q", d, err, digest)
	}
	for nome, doc := range map[string][]byte{
		"sem o campo do digest":           semCampo,
		"veredicto repetido la dentro":    bytes.Replace(cru, []byte(`"veredicto": "qualificado"`), []byte(`"veredicto": "recusado", "veredicto": "qualificado"`), 1),
		"membro do topo repetido":         bytes.Replace(cru, []byte(`"modo":`), []byte(`"modo": "real", "modo":`), 1),
		"campo do digest repetido":        bytes.Replace(cru, []byte(`"`+CampoDoDigestDoRelatorio+`":`), []byte(`"`+CampoDoDigestDoRelatorio+`": "`+digest+`", "`+CampoDoDigestDoRelatorio+`":`), 1),
		"campo do digest que nao e texto": bytes.Replace(cru, []byte(`"`+digest+`"`), []byte(`123`), 1),
		"dados depois do documento":       append(append([]byte(nil), cru...), []byte(`{"veredicto":"qualificado"}`)...),
		// So diferem na caixa: o encoding/json do Go casa os dois com o mesmo campo, e ganha o ultimo.
		"veredicto repetido noutra caixa": bytes.Replace(cru, []byte(`"veredicto": "qualificado"`), []byte(`"veredicto": "qualificado", "Veredicto": "recusado"`), 1),
		"campo do digest noutra caixa":    bytes.Replace(cru, []byte(`"`+CampoDoDigestDoRelatorio+`":`), []byte(`"Digest_Do_Relatorio":`), 1),
		"UTF-8 invalido num texto":        bytes.Replace(cru, []byte(`"modo": "falso"`), []byte("\"modo\": \"fal\xffso\""), 1),
		"meio par substituto isolado":     bytes.Replace(cru, []byte(`"modo": "falso"`), []byte(`"modo": "falso`+"\\"+`ud800"`), 1),
		"caracter de substituicao":        bytes.Replace(cru, []byte(`"modo": "falso"`), []byte("\"modo\": \"falso\xef\xbf\xbd\""), 1),
		"uma lista":                       []byte(`[1,2,3]`),
		"vazio":                           nil,
		"JSON partido":                    cru[:len(cru)/2],
	} {
		if _, err := VerificarDigestDoRelatorio(doc); !errors.Is(err, ErrDigestDoRelatorio) {
			t.Errorf("%s: a verificacao aceitou (%v)", nome, err)
		}
	}
	// O campo do digest so se retira NO TOPO: um membro com o mesmo nome la dentro e conteudo, e conta.
	dentro := bytes.Replace(cru, []byte(`"regra":`), []byte(`"`+CampoDoDigestDoRelatorio+`": "x", "regra":`), 1)
	if _, err := VerificarDigestDoRelatorio(dentro); !errors.Is(err, ErrDigestDoRelatorio) {
		t.Errorf("um membro com o nome do digest fora do topo foi ignorado")
	}
}

// A forma canonica, em exemplos pequenos e escritos a mao: e a definicao que outro leitor (o no,
// no AOS-519) tem de reproduzir.
func TestAOS518_Digest_FormaCanonica(t *testing.T) {
	// `@` faz de barra invertida seguida de `u`: assim o ficheiro de teste nao tem escapes Unicode.
	u := func(s string) string { return strings.ReplaceAll(s, "@", "\\"+"u") }
	for entrada, quer := range map[string]string{
		`{"b":1,"a":2}`: `{"a":2,"b":1}`,
		"{\n  \"b\" : [ 1 , 2.50 , 1e3 ],\n  \"a\" : null\n}":           `{"a":null,"b":[1,2.50,1e3]}`,
		`{"z":{"y":true,"x":false},"digest_do_relatorio":"sha256:abc"}`: `{"z":{"x":false,"y":true}}`,
		// Os escapes do ficheiro nao contam: o texto e reescrito. `<`, `>` e `&` ficam directos; as
		// aspas, a barra, a quebra de linha e o U+2028 ficam escapados.
		u(`{"t":"a@0041<b>&\n\"\\ @00e7 @2028 @003c"}`):  u(`{"t":"aA<b>&\n\"\\ `) + "\u00e7" + u(` @2028 <"}`),
		`{"zz":1,"e":2,"Z":3,"a":{"b":[{"d":1,"c":2}]}}`: `{"Z":3,"a":{"b":[{"c":2,"d":1}]},"e":2,"zz":1}`,
		`{}`: `{}`,
	} {
		tem, err := FormaCanonicaDoRelatorio([]byte(entrada))
		if err != nil || string(tem) != quer {
			t.Errorf("forma canonica de %s = %s (%v), quer %s", entrada, tem, err, quer)
		}
	}
	// Tres textos DIFERENTES que o leitor de JSON reduz ao mesmo (um meio par substituto isolado
	// vira U+FFFD): sem a recusa davam a mesma forma canonica, e portanto o mesmo digest.
	for _, entrada := range []string{u(`{"t":"a@d800"}`), u(`{"t":"a@dfff"}`), `{"t":"a` + string(rune(0xFFFD)) + `"}`, u(`{"a@d800":1}`)} {
		if tem, err := FormaCanonicaDoRelatorio([]byte(entrada)); !errors.Is(err, ErrDigestDoRelatorio) {
			t.Errorf("forma canonica de %q = %s (%v); tinha de recusar", entrada, tem, err)
		}
	}
	// Um par substituto INTEIRO e um caracter como os outros.
	if tem, err := FormaCanonicaDoRelatorio([]byte(u(`{"t":"@d83d@de00"}`))); err != nil || len(tem) != len(`{"t":""}`)+4 {
		t.Errorf("um par substituto inteiro foi recusado ou mal escrito: %q (%v)", tem, err)
	}
}

// O DIGEST DA INTEGRIDADE, NAO AUTENTICIDADE — e o relatorio di-lo. Quem altera o veredicto e
// recalcula o digest fica com um ficheiro que verifica: a verificacao so prova que o ficheiro e
// coerente consigo proprio. A autenticidade vem da assinatura do registo que o cita (AOS-519).
func TestAOS518_Digest_IntegridadeNaoAutenticidade(t *testing.T) {
	cru := aos518RelatorioEscrito(t)
	original, _ := VerificarDigestDoRelatorio(cru)
	forjado := bytes.Replace(cru, []byte(`"nos_com_tools_cumpridos": 40`), []byte(`"nos_com_tools_cumpridos": 41`), 1)
	novo, err := DigestDoRelatorio(forjado)
	if err != nil || novo == original {
		t.Fatalf("digest do forjado: %q, %v", novo, err)
	}
	forjado = bytes.Replace(forjado, []byte(original), []byte(novo), 1)
	if d, err := VerificarDigestDoRelatorio(forjado); err != nil || d != novo {
		t.Fatalf("um relatorio alterado com o digest recalculado deixou de verificar (%v): o limite documentado ja nao e este", err)
	}
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	if !contem(r.QualificacaoDoModelo.NaoProva, NaoProvaAutenticidade) || !strings.Contains(strings.Join(r.Limites, "\n"), "INTEGRIDADE, nao autenticidade") {
		t.Errorf("o relatorio nao diz que o digest so da integridade")
	}
}

// Dois relatorios de qualificacao com o mesmo nome (o mesmo segundo, a mesma pasta): o segundo
// FALHA em vez de escrever por cima — um relatorio citado pelo digest nao se substitui.
func TestAOS518_Digest_NaoSeEscrevePorCimaDeUmRelatorioDeQualificacao(t *testing.T) {
	saida := t.TempDir()
	args := []string{"falso", "--experiencia", "qualificacao", "--perfil", aos516Ficheiro(t, aos518PerfilSimples), "--roteiro", "cumpre", "--saida", saida, "--silencioso"}
	if e := executar(t, Ambiente{}, args...); e.codigo != SaidaQualificadoEmEnsaio {
		t.Fatalf("primeira: codigo %d", e.codigo)
	}
	antes := aos518Relatorios(t, saida)
	cru, _ := os.ReadFile(antes[0])
	e := executar(t, Ambiente{}, args...) // o relogio dos testes e fixo: o mesmo nome
	if e.codigo != SaidaErro || !strings.Contains(e.stderr, "nao se escreve por cima") {
		t.Errorf("a segunda, com o mesmo nome: codigo %d\n%s", e.codigo, e.stderr)
	}
	if depois, _ := os.ReadFile(antes[0]); !bytes.Equal(cru, depois) || len(aos518Relatorios(t, saida)) != 1 {
		t.Errorf("o relatorio existente foi alterado")
	}
}
