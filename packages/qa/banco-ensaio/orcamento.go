package bancoensaio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// O ORÇAMENTO DE UMA QUALIFICAÇÃO (AOS-518, decisão D7 do dono, 2026-10-11).
//
// Três tectos, todos ao lado do tecto do dia, que se mantém e continua a contar pelo PIOR caso:
//
//   - 300 pedidos por qualificação ([LimiteDaQualificacao]), contados antes de cada envio;
//   - 3 USD estimados por qualificação, quando o fornecedor tem tecto em dólares no ficheiro de
//     chaves (é o que faz o contador estimar o gasto);
//   - duas qualificações por perfil por dia ([RegistarQualificacao]): repetir até passar é
//     escolher o resultado.

const (
	// TectoDePedidosDaQualificacao é o máximo de pedidos que uma qualificação envia.
	TectoDePedidosDaQualificacao = 300
	// TectoMicroUSDDaQualificacao é o máximo de gasto ESTIMADO de uma qualificação (3 USD).
	TectoMicroUSDDaQualificacao = 3_000_000
	// QualificacoesPorPerfilPorDia é o máximo de qualificações do mesmo perfil no mesmo dia (UTC).
	QualificacoesPorPerfilPorDia = 2
	// PassagensDoControlo é o número de passagens pela bateria da corrida de controlo sem devolução.
	PassagensDoControlo = 1
)

// ErrQualificacaoSemPerfil — a qualificação é de uma rota COM o seu perfil: sem perfil não há
// modelo esperado com que comparar o servido, nem digest que o veredicto possa citar.
var ErrQualificacaoSemPerfil = errors.New("banco-ensaio: a experiencia qualificacao exige um perfil candidato (--perfil)")

// LimiteDaQualificacao é o tecto próprio de pedidos de uma qualificação. Cada pedido é contado
// ANTES de sair, no transporte do nó de ensaio; ao chegar ao máximo, o pedido seguinte não sai.
type LimiteDaQualificacao struct {
	maximo int64

	mu       sync.Mutex
	enviados int64
	atingido bool
}

// NovoLimiteDaQualificacao devolve um limite com o máximo dado (zero ou negativo ⇒ nada sai).
func NovoLimiteDaQualificacao(maximo int64) *LimiteDaQualificacao {
	return &LimiteDaQualificacao{maximo: maximo}
}

// Reservar conta um pedido. Com o máximo já atingido devolve [ErrTectoAtingido]: o pedido não sai.
func (l *LimiteDaQualificacao) Reservar() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.enviados >= l.maximo {
		l.atingido = true
		return fmt.Errorf("%w: pedidos da qualificacao", ErrTectoAtingido)
	}
	l.enviados++
	return nil
}

// Enviados devolve quantos pedidos o limite deixou sair.
func (l *LimiteDaQualificacao) Enviados() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enviados
}

// Atingido diz se algum pedido foi recusado por este limite.
func (l *LimiteDaQualificacao) Atingido() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.atingido
}

// OrcamentoDaQualificacao é o orçamento de uma qualificação e o que ela gastou. Vai no relatório.
type OrcamentoDaQualificacao struct {
	// PiorCasoDeclarado é o máximo de pedidos que o plano podia fazer — a corrida, o controlo e a
	// sonda —, declarado antes do primeiro pedido e conferido contra o que restava do dia.
	PiorCasoDeclarado int64 `json:"pior_caso_declarado_de_pedidos"`
	// TectoDePedidos é o tecto próprio da qualificação; PedidosEnviados, o que ela enviou.
	TectoDePedidos  int64 `json:"tecto_de_pedidos_da_qualificacao"`
	PedidosEnviados int64 `json:"pedidos_enviados"`
	// TectoMicroUSD é o tecto de gasto estimado da qualificação. MicroUSDEstimado é o gasto
	// estimado desta corrida; nil quando o fornecedor não tem tecto em dólares (não há estimativa).
	TectoMicroUSD    int64  `json:"tecto_micro_usd_da_qualificacao"`
	MicroUSDEstimado *int64 `json:"micro_usd_estimado_desta_qualificacao"`
	// QualificacoesDoPerfilHoje é o número de ordem desta qualificação entre as do mesmo perfil no
	// mesmo dia; nil fora do modo real, que é o único com esse contador.
	QualificacoesDoPerfilHoje *int `json:"qualificacoes_do_perfil_hoje"`
	MaximoPorPerfilPorDia     int  `json:"maximo_de_qualificacoes_por_perfil_por_dia"`
}

// ErrQualificacoesDoDia — o perfil já foi qualificado o máximo de vezes hoje.
var ErrQualificacoesDoDia = errors.New("banco-ensaio: este perfil ja foi qualificado o maximo de vezes hoje (causa: qualificacoes_do_dia_esgotadas); repetir ate passar e escolher o resultado")

// ErrRegistoDeQualificacoes — o registo das qualificações do dia existe e não se lê. Não se
// recomeça do zero: repara-se à mão.
var ErrRegistoDeQualificacoes = errors.New("banco-ensaio: o registo das qualificacoes do dia nao se le")

// NomeDoRegistoDeQualificacoes é o ficheiro, ao lado do ficheiro de chaves, que conta as
// qualificações por perfil e por dia.
const NomeDoRegistoDeQualificacoes = "qualificacoes.json"

type registoDeQualificacoes struct {
	// Dias: dia (UTC) → digest do perfil → qualificações começadas.
	Dias map[string]map[string]int `json:"dias"`
}

func lerRegistoDeQualificacoes(caminho string) (registoDeQualificacoes, error) {
	r := registoDeQualificacoes{Dias: map[string]map[string]int{}}
	cru, err := os.ReadFile(caminho) // #nosec G304 -- caminho derivado do ficheiro de chaves do dono
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, ErrRegistoDeQualificacoes
	}
	dec := json.NewDecoder(strings.NewReader(string(cru)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || r.Dias == nil {
		return r, ErrRegistoDeQualificacoes
	}
	for _, perfis := range r.Dias {
		for _, n := range perfis {
			if n < 0 {
				return r, ErrRegistoDeQualificacoes
			}
		}
	}
	return r, nil
}

// QualificacoesDeHoje devolve quantas qualificações do perfil já começaram no dia dado. Não escreve.
func QualificacoesDeHoje(caminho, dia, perfilDigest string) (int, error) {
	r, err := lerRegistoDeQualificacoes(caminho)
	if err != nil {
		return 0, err
	}
	return r.Dias[dia][perfilDigest], nil
}

// RegistarQualificacao conta mais uma qualificação do perfil no dia dado e devolve o seu número de
// ordem. Com o máximo já atingido devolve [ErrQualificacoesDoDia] e não escreve nada. Chama-se
// depois de a sonda passar e ANTES do primeiro caso, com o contador do dia aberto (é a trava dele
// que garante um processo de cada vez).
func RegistarQualificacao(caminho, dia, perfilDigest string) (int, error) {
	r, err := lerRegistoDeQualificacoes(caminho)
	if err != nil {
		return 0, err
	}
	n := r.Dias[dia][perfilDigest]
	if n >= QualificacoesPorPerfilPorDia {
		return n, ErrQualificacoesDoDia
	}
	// Só o dia de hoje fica: o registo não cresce.
	r.Dias = map[string]map[string]int{dia: r.Dias[dia]}
	if r.Dias[dia] == nil {
		r.Dias[dia] = map[string]int{}
	}
	r.Dias[dia][perfilDigest] = n + 1
	cru, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return n, err
	}
	temporario := caminho + ".tmp"
	if err := os.WriteFile(temporario, append(cru, '\n'), 0o600); err != nil {
		return n, fmt.Errorf("%w: %v", ErrRegistoDeQualificacoes, filepath.Base(temporario))
	}
	if err := os.Rename(temporario, caminho); err != nil {
		return n, fmt.Errorf("%w: %v", ErrRegistoDeQualificacoes, filepath.Base(caminho))
	}
	return n + 1, nil
}

// ImagemDoProxyDeProducao é a imagem do proxy DECLARADA para produção, pelo digest (LiteLLM
// 1.96.2; lida no servidor a 2026-10-11, é para onde a tag do compose resolvia; o AOS-517 fixa-a
// no compose). Uma qualificação feita atrás de outra imagem não vale para produção: a qualificação
// é da rota, e a imagem do proxy é um dos quatro factores dela.
const ImagemDoProxyDeProducao = "ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63"

// ErrImagemDoProxy — a imagem do proxy em uso não é a declarada para produção.
var ErrImagemDoProxy = errors.New("banco-ensaio: a imagem do proxy em uso nao e a declarada para producao (causa: imagem_do_proxy_nao_e_a_de_producao); a qualificacao nao arranca")

// conferirImagemDoProxy recusa uma qualificação cuja imagem do proxy não é a de produção.
func conferirImagemDoProxy(emUso, declarada string) error {
	if emUso == "" || emUso != declarada || !strings.Contains(emUso, "@sha256:") {
		return ErrImagemDoProxy
	}
	return nil
}

// digestDaImagem devolve a parte `sha256:…` de uma referência de imagem fixada pelo digest.
func digestDaImagem(referencia string) string {
	if _, digest, tem := strings.Cut(referencia, "@"); tem {
		return digest
	}
	return ""
}
