package jetstream

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// logica_test.go — a lógica de DECISÃO do adaptador, testada SEM cluster.
//
// # Porque estes testes existem em separado
//
// A correcção do adaptador contra um substrato real só se prova contra um substrato real,
// e é por isso que quase tudo aqui é `t.Skip` sem `AOS_NATS_URL`. Mas nem tudo precisa de
// rede: a validação de um stream_id, a derivação do prefixo, a verificação da fronteira de
// soberania e a classificação de uma recusa são decisões PURAS — e são precisamente as que
// decidem se dados vão parar onde não podem.
//
// Deixá-las cobertas só por testes que a CI salta seria ter as regras de segurança do
// adaptador sem rede de segurança nenhuma no sítio onde ela corre sempre.

func TestSubjectDe_RecusaOQueNaoERepresentavel(t *testing.T) {
	s := &Store{prefixo: "aos.es.teste"}
	maus := map[string]string{
		"vazio":         "",
		"com ponto":     "run.com.ponto",
		"com espaço":    "run com espaco",
		"curinga *":     "run*",
		"curinga >":     "run>",
		"com tabulação": "run\tx",
		"com CR":        "run\rx",
		"com LF":        "run\nx",
	}
	for nome, mau := range maus {
		if _, err := s.subjectDe(mau); err == nil {
			t.Errorf("%s: o stream_id %q foi aceite — seria escapado para um subject vizinho, "+
				"onde outro stream leria os nossos eventos", nome, mau)
		} else if !errors.Is(err, eventstore.ErrConfig) {
			t.Errorf("%s: erro = %v, quer E_CONFIG", nome, err)
		}
	}

	// E o que É representável passa — incluindo o `lease:<run>` de AOS-018, que é o
	// stream de que toda a disciplina de posse depende.
	for _, bom := range []string{"run-1", "lease:run-1", "run_2", "RUN-3"} {
		got, err := s.subjectDe(bom)
		if err != nil {
			t.Errorf("stream_id %q recusado: %v", bom, err)
			continue
		}
		if want := "aos.es.teste." + bom; got != want {
			t.Errorf("subject de %q = %q, quer %q", bom, got, want)
		}
	}
}

func TestPrefixoDe_DerivaDoNomeEEvitaColisoes(t *testing.T) {
	// O DEFEITO que a derivação fecha: com um prefixo constante, dois streams no mesmo
	// cluster declaravam ambos `aos.es.>` e o servidor recusava o segundo.
	a, b := prefixoDe("AOS_EVENTS"), prefixoDe("AOS_OUTRO")
	if a == b {
		t.Fatalf("dois streams distintos derivaram o MESMO prefixo (%q) — voltariam a colidir", a)
	}
	if !strings.HasPrefix(a, PrefixoSubjectOmissao+".") {
		t.Errorf("prefixo %q não assenta na raiz %q", a, PrefixoSubjectOmissao)
	}
	// Nada que não seja representável num subject pode sobreviver à derivação.
	sujo := prefixoDe("Nome.Com Pontos*E>Curingas")
	if strings.ContainsAny(strings.TrimPrefix(sujo, PrefixoSubjectOmissao+"."), ". *>") {
		t.Errorf("a derivação deixou passar um carácter não representável: %q", sujo)
	}
	if err := validarPrefixo(sujo); err != nil {
		t.Errorf("o prefixo derivado devia ser sempre válido, veio %v", err)
	}
}

func TestEnderecos_ParteEIgnoraVazios(t *testing.T) {
	got := enderecos(" a:1 , ,b:2,, c:3 ")
	want := []string{"a:1", "b:2", "c:3"}
	if len(got) != len(want) {
		t.Fatalf("enderecos = %v, quer %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("enderecos = %v, quer %v", got, want)
		}
	}
	if len(enderecos("  ,  ")) != 0 {
		t.Error("uma lista só de vazios devia dar zero endereços")
	}
}

func TestFronteira_RegiaoAusenteNega(t *testing.T) {
	// Região ausente é região DESCONHECIDA, e desconhecida NEGA — a mesma postura do PDP.
	for _, vazia := range []string{"", "   ", "\t"} {
		c := config{fronteira: true, regiao: vazia}
		if err := c.validarFronteira(); !errors.Is(err, eventstore.ErrSovereigntyViolation) {
			t.Errorf("fronteira com região %q: erro = %v, quer E_SOVEREIGNTY_VIOLATION", vazia, err)
		}
	}
	// Sem fronteira declarada, uma região vazia é irrelevante.
	if err := (config{}).validarFronteira(); err != nil {
		t.Errorf("sem fronteira não devia validar nada: %v", err)
	}
}

func TestColocacaoExigida_SoComFronteira(t *testing.T) {
	if p := (config{}).colocacaoExigida(); p != nil {
		t.Errorf("sem fronteira a colocação devia ser nil (sem restrição), veio %+v", p)
	}
	p := config{fronteira: true, regiao: "  EU-West "}.colocacaoExigida()
	if p == nil || len(p.Tags) != 1 || p.Tags[0] != "region:eu-west" {
		t.Fatalf("colocação = %+v, quer a tag region:eu-west (normalizada)", p)
	}
}

func TestVerificarColocacao_TresModosDeFalha(t *testing.T) {
	const regiao = "eu-west"

	// (1) O caso SILENCIOSO: stream sem colocação nenhuma.
	err := verificarColocacao(natsjs.StreamConfigLida{}, regiao, "S")
	if !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("stream SEM colocação: erro = %v, quer E_SOVEREIGNTY_VIOLATION", err)
	}
	if !strings.Contains(err.Error(), "SEM colocação") {
		t.Errorf("o erro não diz que o stream está sem colocação: %v", err)
	}

	// (2) Colocação de OUTRA região.
	outra := natsjs.StreamConfigLida{Placement: &natsjs.Placement{Tags: []string{"region:us-east"}}}
	if err := verificarColocacao(outra, regiao, "S"); !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("colocação noutra região: erro = %v, quer E_SOVEREIGNTY_VIOLATION", err)
	}

	// (3) Tags vazias contam como ausência, não como «tudo bem».
	vazia := natsjs.StreamConfigLida{Placement: &natsjs.Placement{}}
	if err := verificarColocacao(vazia, regiao, "S"); !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("colocação com tags vazias: erro = %v, quer E_SOVEREIGNTY_VIOLATION", err)
	}

	// E o caso conforme passa, mesmo com a tag entre outras.
	ok := natsjs.StreamConfigLida{Placement: &natsjs.Placement{Tags: []string{"az:1", "region:eu-west"}}}
	if err := verificarColocacao(ok, regiao, "S"); err != nil {
		t.Fatalf("colocação conforme foi recusada: %v", err)
	}
}

func TestRegistarRecusa_NomeiaACausa(t *testing.T) {
	// Um span que só dissesse «rejected» mandaria toda a gente ler os logs.
	espiao := &spanDeTeste{attrs: map[string]any{}}
	registarRecusa(espiao, eventstore.ErrAppendOnlyViolation)
	if espiao.attrs[eventstore.AtributoDesfecho] != "rejected" {
		t.Errorf("desfecho = %v, quer rejected", espiao.attrs[eventstore.AtributoDesfecho])
	}
	if espiao.attrs[eventstore.AtributoErro] != "E_APPEND_ONLY_VIOLATION" {
		t.Errorf("erro = %v, quer o código canónico E_APPEND_ONLY_VIOLATION", espiao.attrs[eventstore.AtributoErro])
	}

	// Um erro que não é do contrato também tem de deixar rasto legível.
	outro := &spanDeTeste{attrs: map[string]any{}}
	registarRecusa(outro, errors.New("rede em baixo"))
	if outro.attrs[eventstore.AtributoErro] != "rede em baixo" {
		t.Errorf("erro não-sentinela = %v, quer a mensagem", outro.attrs[eventstore.AtributoErro])
	}
}

type spanDeTeste struct{ attrs map[string]any }

func (s *spanDeTeste) Atributo(k string, v any) { s.attrs[k] = v }
func (s *spanDeTeste) Fim()                     {}

func TestOpcoes_AplicamSeENaoDesarmamOsDefaults(t *testing.T) {
	c := config{
		stream: NomeStreamPorOmissao, prazo: PrazoPorOmissao, replicas: ReplicasPorOmissao,
		criar: true, obs: observadorNulo{}, rastro: eventstore.NopRastreador{},
	}
	for _, o := range []Option{
		ComNomeDeStream("X"), ComPrefixoDeSubject("p.q"), ComPrazo(3),
		ComReplicas(5), SemCriarStream(), ComBoardDeSoberania("b", "R"),
		ComObservador(nil), ComRastreador(nil), // nil NÃO pode desarmar o default
	} {
		o(&c)
	}
	if c.stream != "X" || c.prefixo != "p.q" || c.prazo != 3 || c.replicas != 5 || c.criar {
		t.Errorf("opções não aplicadas: %+v", c)
	}
	if !c.fronteira || c.board != "b" || c.regiao != "R" {
		t.Errorf("board de soberania não aplicado: %+v", c)
	}
	// Injectar nil tem de deixar os defaults de pé — senão o caminho quente ganhava um
	// nil e um panic.
	if c.obs == nil || c.rastro == nil {
		t.Fatal("um observador/rastreador nil desarmou o default; o caminho quente ficaria com nil")
	}
	c.obs.AppendCommitted("s", 1, 0)
	c.obs.AppendDuplicate("s", 1)
	c.obs.AppendRejected("s", errors.New("x"))
	c.obs.Published("s", 1, 0)
	_, sp := c.rastro.Iniciar(context.Background(), OperacaoDeTeste)
	sp.Atributo("k", 1)
	sp.Fim()
}

// OperacaoDeTeste existe só para o nop acima; usar uma operação real aqui daria a ideia
// errada de que este teste mede emissão.
const OperacaoDeTeste = "teste"

func TestLigarRastreador_JanelaFechaNoPrimeiroUso(t *testing.T) {
	s := &Store{streams: map[string]*estado{}, subs: map[string]*subscricao{},
		rastro: eventstore.NopRastreador{}}
	if err := s.LigarRastreador(spanDeTesteRastreador{}); err != nil {
		t.Fatalf("ligar antes de usar: %v", err)
	}
	if err := s.LigarRastreador(nil); err != nil {
		t.Fatalf("ligar nil devia ser no-op: %v", err)
	}
	s.marcarUsado()
	if err := s.LigarRastreador(spanDeTesteRastreador{}); err == nil {
		t.Fatal("ligar depois do primeiro uso foi aceite — daria spans para umas operações e não outras")
	}
}

type spanDeTesteRastreador struct{}

func (spanDeTesteRastreador) Iniciar(ctx context.Context, _ string) (context.Context, eventstore.Rastro) {
	return ctx, &spanDeTeste{attrs: map[string]any{}}
}

// TestAcessores_RefletemAConfiguracao — os acessores, e a prontidão.
//
// AOS-350: a asserção «um store aberto devia estar Healthy» foi INVERTIDA, e a inversão é
// o sensor. Este store é construído por literal, SEM cliente NATS. Antes, `Healthy()` era
// `!s.estaFechado()` e um store sem ligação nenhuma dizia-se saudável — que é exactamente
// a crença que o ticket remove: um cliente desligado devolve [natsjs.ErrDesligado] a todas
// as escritas sem elas saírem, e a prontidão tem de o dizer.
//
// O ramo POSITIVO — ligação viva ⇒ Healthy — não é construível daqui: exige um socket, e
// os campos de [natsjs.Conn] são privados ao seu pacote. Vive na suite com cluster
// (`conformidade_test.go`, sob `AOS_NATS_URL`), e fica declarado que aqui NÃO é medido.
func TestAcessores_RefletemAConfiguracao(t *testing.T) {
	s := &Store{regiao: "eu-west", board: "b1", streams: map[string]*estado{}, subs: map[string]*subscricao{}}
	if s.Region() != "eu-west" || s.SovereigntyBoard() != "b1" {
		t.Errorf("acessores = (%q,%q)", s.Region(), s.SovereigntyBoard())
	}
	if s.Healthy() {
		t.Error("um store SEM LIGAÇÃO disse-se Healthy — é o defeito de AOS-350: " +
			"o /readyz fica 200 verde sobre um substrato que recusa todas as escritas")
	}
	s.fechado = true
	if s.Healthy() {
		t.Error("um store fechado NÃO pode dizer-se Healthy — é o que serve /readyz")
	}
	if s.Streams() != nil {
		t.Error("um store fechado não pode listar streams")
	}
}
