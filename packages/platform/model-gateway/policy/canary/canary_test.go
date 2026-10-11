package canary

import (
	"fmt"
	"math"
	"testing"
)

// vectores são os VECTORES FIXOS da função de escolha, os mesmos que estão escritos no ADR do
// AOS-520 (§2.3). Mudar a função — o resumo, o prefixo de domínio, os bytes lidos, o módulo ou a
// comparação — avermelha-os. Não se actualizam para acompanhar uma mudança: uma função que reparta
// de outra maneira é outra versão ([Version]), com os seus próprios vectores.
var vectores = []struct {
	chave string
	balde uint64
	// candidato a 10%, a 25% e a 50%.
	a10, a25, a50 bool
}{
	{"0", 168, true, true, true},
	{"Plano-0001", 4773, false, false, true},
	{"aos-internal/x", 6242, false, false, false},
	{"p~no~2", 6937, false, false, false},
	{"a", 8230, false, false, false},
	{"plano-0001", 8680, false, false, false},
	{"plano com espaco", 8712, false, false, false},
	{"run-2026-10-11", 9451, false, false, false},
}

func TestAOS520_Canary_VectoresFixosDoADR(t *testing.T) {
	for _, v := range vectores {
		if got := Bucket(v.chave); got != v.balde {
			t.Errorf("Bucket(%q) = %d, o ADR diz %d — a funcao de escolha mudou", v.chave, got, v.balde)
		}
		for _, c := range []struct {
			fatia     int
			candidato bool
		}{{10, v.a10}, {25, v.a25}, {50, v.a50}} {
			want := RoleIncumbent
			if c.candidato {
				want = RoleCandidate
			}
			if got := Choose(v.chave, c.fatia); got != want {
				t.Errorf("Choose(%q, %d) = %q, o ADR diz %q", v.chave, c.fatia, got, want)
			}
		}
	}
}

// A chave entra byte a byte: nem a caixa nem um `~` a mudam ou a partem. "Plano-0001" e
// "plano-0001" são dois planos, e "p~no~2" não é o plano "p".
func TestAOS520_Canary_AChaveNaoENormalizadaNemPartida(t *testing.T) {
	if Bucket("Plano-0001") == Bucket("plano-0001") {
		t.Fatal("a caixa da chave nao pode ser normalizada")
	}
	if Bucket("p~no~2") == Bucket("p") {
		t.Fatal("a chave nao pode ser partida pelo `~`")
	}
}

// A versão está no domínio do resumo: o balde de uma chave sob este prefixo não é o de um resumo
// sem prefixo, nem o de um prefixo sem o byte separador.
func TestAOS520_Canary_OPrefixoDeDominioEAVersao(t *testing.T) {
	if Version != "aos-canary/v1" {
		t.Fatalf("Version = %q — mudar a versao e mudar a funcao: os vectores e o ADR mudam com ela", Version)
	}
	if dominio != "aos-canary/v1\x00" {
		t.Fatalf("dominio = %q", dominio)
	}
}

// Os dois planos em cima da fronteira dos 10%: baldes 1000 e 999 (vectores do ADR, §2.3).
const (
	planoNaFronteira       = "fronteira-12733"
	planoAbaixoDaFronteira = "fronteira-16149"
)

// ids devolve a série FIXA de identificadores de plano dos testes de distribuição.
func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("plano-%04d", i)
	}
	return out
}

// contar devolve quantos dos ids a função `escolhe` dá ao candidato com a fatia dada.
func contar(escolhe func(string, int) Role, chaves []string, fatia int) int {
	n := 0
	for _, k := range chaves {
		if escolhe(k, fatia) == RoleCandidate {
			n++
		}
	}
	return n
}

// intervalo3Sigma é o intervalo esperado da contagem do candidato numa amostra de n planos com a
// fatia dada: a média mais ou menos três desvios-padrão de uma binomial, arredondado para dentro.
func intervalo3Sigma(n, fatia int) (min, max int) {
	p := float64(fatia) / 100
	media := float64(n) * p
	sigma := math.Sqrt(float64(n) * p * (1 - p))
	return int(math.Ceil(media - 3*sigma)), int(math.Floor(media + 3*sigma))
}

// dentroDoEsperado é a MEDIÇÃO dos testes de distribuição: a contagem tem de cair no intervalo de
// três sigma da fatia. Está numa função para os controlos negativos a poderem chamar com uma
// função de escolha avariada.
func dentroDoEsperado(escolhe func(string, int) Role, chaves []string, fatia int) (contagem int, ok bool) {
	min, max := intervalo3Sigma(len(chaves), fatia)
	c := contar(escolhe, chaves, fatia)
	return c, c >= min && c <= max
}

// distribuicoes são as fatias medidas, com o intervalo de três sigma para 1000 planos (escrito, e
// conferido contra a fórmula) e a contagem EXACTA que a função dá sobre a série fixa — presa: é
// outro vector da função.
var distribuicoes = []struct {
	fatia, min, max, exacta int
}{
	{10, 72, 128, 110},
	{25, 209, 291, 254},
	{50, 453, 547, 498},
}

func TestAOS520_Canary_Distribuicao_1000Planos(t *testing.T) {
	chaves := ids(1000)
	for _, d := range distribuicoes {
		if min, max := intervalo3Sigma(len(chaves), d.fatia); min != d.min || max != d.max {
			t.Fatalf("fatia %d%%: o intervalo escrito [%d,%d] nao e o de 3 sigma [%d,%d]", d.fatia, d.min, d.max, min, max)
		}
		c, ok := dentroDoEsperado(Choose, chaves, d.fatia)
		if !ok {
			t.Errorf("fatia %d%%: %d planos do candidato em %d, fora de [%d,%d]", d.fatia, c, len(chaves), d.min, d.max)
		}
		if c != d.exacta {
			t.Errorf("fatia %d%%: %d planos do candidato, a contagem presa e %d — a funcao de escolha mudou", d.fatia, c, d.exacta)
		}
	}
}

// O critério do ticket fala em «pelo menos 200 identificadores»: a 10%, os primeiros 200 da série.
func TestAOS520_Canary_Distribuicao_200PlanosA10(t *testing.T) {
	chaves := ids(200)
	min, max := intervalo3Sigma(200, 10)
	if min != 8 || max != 32 {
		t.Fatalf("intervalo de 3 sigma para 200 planos a 10%% = [%d,%d], esperado [8,32]", min, max)
	}
	c, ok := dentroDoEsperado(Choose, chaves, 10)
	if !ok || c != 25 {
		t.Fatalf("200 planos a 10%%: %d do candidato (dentro do intervalo: %v); a contagem presa e 25", c, ok)
	}
}

// CONTROLO NEGATIVO DA MEDIÇÃO. A mesma medição, com uma função que devolve sempre o titular e com
// uma que devolve sempre o candidato, tem de FALHAR em todas as fatias medidas: o intervalo não é
// largo ao ponto de deixar passar uma função que não reparte.
func TestAOS520_Canary_ControloNegativo_SempreTitularESempreCandidato(t *testing.T) {
	sempreTitular := func(string, int) Role { return RoleIncumbent }
	sempreCandidato := func(string, int) Role { return RoleCandidate }
	for _, n := range []int{200, 1000} {
		chaves := ids(n)
		for _, d := range distribuicoes {
			if c, ok := dentroDoEsperado(sempreTitular, chaves, d.fatia); ok {
				t.Errorf("%d planos, fatia %d%%: «sempre titular» (%d) passou a medicao", n, d.fatia, c)
			}
			if c, ok := dentroDoEsperado(sempreCandidato, chaves, d.fatia); ok {
				t.Errorf("%d planos, fatia %d%%: «sempre candidato» (%d) passou a medicao", n, d.fatia, c)
			}
		}
	}
}

func TestAOS520_Canary_Extremos_0E100(t *testing.T) {
	chaves := ids(1000)
	if c := contar(Choose, chaves, 0); c != 0 {
		t.Fatalf("fatia 0%%: %d planos do candidato, esperado 0", c)
	}
	if c := contar(Choose, chaves, 100); c != len(chaves) {
		t.Fatalf("fatia 100%%: %d planos do candidato, esperado %d", c, len(chaves))
	}
}

// MONÓTONA: quem é do candidato com uma fatia continua a sê-lo com qualquer fatia maior. Subir a
// fatia nunca tira um plano ao candidato.
func TestAOS520_Canary_InclusaoEntreFatias(t *testing.T) {
	chaves := ids(1000)
	for fatia := 0; fatia < 100; fatia++ {
		for _, k := range chaves {
			if Choose(k, fatia) == RoleCandidate && Choose(k, fatia+1) != RoleCandidate {
				t.Fatalf("plano %q: candidato a %d%% e titular a %d%%", k, fatia, fatia+1)
			}
		}
	}
}

// A fronteira da comparação: o balde 999 é do candidato a 10% (999 < 1000) e o balde 1000 não é.
// Presa pelo primeiro plano da série que cai em cada um dos dois lados da fronteira, sem depender
// de existir um plano exactamente em cima dela.
func TestAOS520_Canary_AFronteiraEEstrita(t *testing.T) {
	for _, k := range ids(1000) {
		b := Bucket(k)
		for _, fatia := range []int{1, 10, 25, 50, 99} {
			want := RoleIncumbent
			if b < uint64(fatia)*bucketsPerPercent {
				want = RoleCandidate
			}
			if got := Choose(k, fatia); got != want {
				t.Fatalf("Choose(%q, %d) = %q com o balde %d", k, fatia, got, b)
			}
		}
	}
	// [planoNaFronteira] cai no balde 1000: a 10% é do titular. Com `<=` passava a candidato.
	if b := Bucket(planoNaFronteira); b != 1000 {
		t.Fatalf("Bucket(%q) = %d, esperado 1000 (o plano em cima da fronteira dos 10%%)", planoNaFronteira, b)
	}
	if Choose(planoNaFronteira, 10) != RoleIncumbent {
		t.Fatal("o balde 1000 nao e do candidato a 10%: a comparacao e estrita")
	}
	if Choose(planoNaFronteira, 11) != RoleCandidate {
		t.Fatal("o balde 1000 e do candidato a 11%")
	}
	// E [planoAbaixoDaFronteira] cai no balde 999: a 10% é do candidato.
	if b := Bucket(planoAbaixoDaFronteira); b != 999 {
		t.Fatalf("Bucket(%q) = %d, esperado 999", planoAbaixoDaFronteira, b)
	}
	if Choose(planoAbaixoDaFronteira, 10) != RoleCandidate {
		t.Fatal("o balde 999 e do candidato a 10%")
	}
}

// Fail-closed nas entradas que não deviam existir: o titular.
func TestAOS520_Canary_EntradasInvalidasDaoOTitular(t *testing.T) {
	for _, fatia := range []int{-1, 101, 1000, math.MinInt, math.MaxInt} {
		if c := contar(Choose, ids(200), fatia); c != 0 {
			t.Errorf("fatia %d: %d planos do candidato, esperado 0", fatia, c)
		}
	}
	if Choose("", 100) != RoleIncumbent {
		t.Error("chave vazia a 100%: esperado o titular")
	}
}

// Determinista: a mesma entrada dá a mesma escolha, chamada as vezes que for.
func TestAOS520_Canary_Determinista(t *testing.T) {
	for _, k := range ids(200) {
		primeiro := Choose(k, 10)
		for i := 0; i < 5; i++ {
			if Choose(k, 10) != primeiro {
				t.Fatalf("Choose(%q, 10) mudou entre chamadas", k)
			}
		}
	}
}
