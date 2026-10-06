package main

import (
	"regexp"
	"strings"
	"testing"
)

// A PROCURA DE CONTEÚDO PROIBIDO NO QUE SAI DO PROCESSO, SEM APANHAR O QUE É VOLÁTIL.
//
// Os testes de «sem conteúdo» (AOS-499, AOS-501) procuram no stdout, no stderr, nas métricas, no
// `detail`, nos eventos e nos corpos dos pedidos uma sentinela que só existe no documento do
// titular. Várias sentinelas são números curtos («4471», «1250», «9999»), e o que sai do processo
// leva números que o teste não escolhe: a porta do nó de teste, o nome da pasta temporária, os
// hashes do plano e dos contratos, instantes em nanossegundos.
//
// A release v0.1.48 ficou vermelha por isso: o sistema operativo deu ao nó de teste a porta 44717,
// e a procura de «4471» por subcadeia leu a porta como conteúdo do documento. O defeito era do
// teste, e era de forma: qualquer sentinela numérica curta procurada por subcadeia tem a mesma
// colisão à espera.
//
// [levaProibido] é a procura que os testes usam. Uma sentinela que NÃO é só dígitos procura-se
// por subcadeia, como antes. Uma sentinela só de dígitos procura-se como NÚMERO INTEIRO: não
// conta dentro de um número maior (a porta 44717) nem dentro de uma sequência hexadecimal longa
// (um hash, um instante em nanossegundos), que saem da procura antes de ela começar.

// sequenciaHexLonga apanha hashes e instantes: dezasseis ou mais caracteres hexadecimais seguidos.
// Dígitos decimais são hexadecimais, pelo que um instante em nanossegundos também cai aqui.
var sequenciaHexLonga = regexp.MustCompile(`[0-9a-fA-F]{16,}`)

// levaProibido diz se `texto` contém a sentinela `proibido`. Ver o comentário do ficheiro.
func levaProibido(texto, proibido string) bool {
	if proibido == "" {
		return false
	}
	if !soDigitos(proibido) {
		return strings.Contains(texto, proibido)
	}
	limpo := sequenciaHexLonga.ReplaceAllString(texto, " ")
	for i := 0; i < len(limpo); {
		j := strings.Index(limpo[i:], proibido)
		if j < 0 {
			return false
		}
		ini, fim := i+j, i+j+len(proibido)
		colaAntes := ini > 0 && eDigito(limpo[ini-1])
		colaDepois := fim < len(limpo) && eDigito(limpo[fim])
		if !colaAntes && !colaDepois {
			return true
		}
		i = ini + 1
	}
	return false
}

func soDigitos(s string) bool {
	for i := 0; i < len(s); i++ {
		if !eDigito(s[i]) {
			return false
		}
	}
	return s != ""
}

func eDigito(b byte) bool { return b >= '0' && b <= '9' }

// TestLevaProibido fixa os dois lados: o que tem de continuar a ser apanhado, e o volátil que não
// pode voltar a ser lido como conteúdo. O primeiro caso negativo é a linha que avermelhou a
// release v0.1.48.
func TestLevaProibido(t *testing.T) {
	casos := []struct {
		nome, texto, proibido string
		quer                  bool
	}{
		{"a porta do no de teste nao e conteudo", "run do no aos em http://127.0.0.1:44717 (sem autenticacao)", "4471", false},
		{"a pasta temporaria nao e conteudo", `documento a guardar em /tmp/TestX3224471025/001/planos/a.plan.json`, "4471", false},
		{"um hash nao e conteudo", "plan_hash=sha256:bda2eac896244471cf56881c8e93dfe433c2203c8bf7f881eba79352bdfacd35c", "4471", false},
		{"um instante em nanossegundos nao e conteudo", `"at_unix_nano":1791226144710000000`, "4471", false},
		{"o numero sozinho e conteudo", "a referencia 4471 ficou por pagar", "4471", true},
		{"o numero entre pontuacao e conteudo", `{"ref":"4471"}`, "4471", true},
		{"o numero no fim e conteudo", "ref=4471", "4471", true},
		{"o numero no inicio e conteudo", "4471 parcelas", "4471", true},
		{"depois de uma falsa ocorrencia vem a verdadeira", "porta 44717 e a referencia 4471", "4471", true},
		{"um numero colado a letras e conteudo", "ref4471x", "4471", true},
		{"uma sentinela de texto procura-se por subcadeia", "o orçamento foi aprovado", "orçamento", true},
		{"uma sentinela de texto dentro de outra palavra conta", "gVisor2", "gVisor", true},
		{"um digest inteiro procura-se por subcadeia", "digest sha256:abcdef0123456789abcdef", "sha256:abcdef0123456789abcdef", true},
		{"sentinela vazia nunca aparece", "qualquer coisa", "", false},
		{"texto sem a sentinela", "nada aqui", "1250", false},
	}
	for _, c := range casos {
		if tem := levaProibido(c.texto, c.proibido); tem != c.quer {
			t.Errorf("%s: levaProibido(%q, %q) = %v, quero %v", c.nome, c.texto, c.proibido, tem, c.quer)
		}
	}
}
