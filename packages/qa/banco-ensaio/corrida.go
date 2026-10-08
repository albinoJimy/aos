package bancoensaio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// A CORRIDA: o plano (que runs, por que ordem), a verificação de que cabe no tecto, e o ciclo.

// Experiencia é o que uma corrida faz. Vocabulário fechado.
type Experiencia string

const (
	// ExperienciaBateria — a bateria T1 a T6, num braço.
	ExperienciaBateria Experiencia = "bateria"
	// ExperienciaSeparadores — a experiência dos separadores: os quatro braços sobre o caso T1,
	// intercalados, um só turno por amostra.
	ExperienciaSeparadores Experiencia = "separadores"
)

// AmostrasDosSeparadores é o tamanho de amostra por braço da experiência dos separadores:
// distingue 10% de 32% com 80% de potência a 5% (desenho A2 §5.3). Efeitos menores não se vêem.
const AmostrasDosSeparadores = 53

// CasoDosSeparadores é o caso sobre o qual a experiência dos separadores corre.
const CasoDosSeparadores = "T1"

// Plano é o plano de uma corrida.
type Plano struct {
	Experiencia Experiencia `json:"experiencia"`
	// Amostras é o número de amostras por braço (separadores) ou de passagens pela bateria.
	Amostras int `json:"amostras"`
	// Semente fixa a ordem dos braços dentro de cada bloco. Vai no relatório.
	Semente uint64 `json:"semente"`
	// Tentativas é o máximo de tentativas por nó (1 = sem nova tentativa).
	Tentativas int `json:"tentativas"`
	// MaxTurnos é o tecto de turnos — e de pedidos — de cada run.
	MaxTurnos int `json:"max_turnos"`
	// Casos são os ids dos casos a correr, pela ordem.
	Casos []string `json:"casos"`
	// Bracos são os braços da corrida.
	Bracos []Braco `json:"bracos"`
}

// PlanoDaBateria devolve o plano de uma corrida da bateria inteira num braço.
func PlanoDaBateria(b *Bateria, braco Braco, amostras int) Plano {
	p := Plano{Experiencia: ExperienciaBateria, Amostras: amostras, Tentativas: 3, MaxTurnos: MaxTurnosPorOmissao, Bracos: []Braco{braco}}
	for _, c := range b.Casos {
		p.Casos = append(p.Casos, c.ID)
	}
	return p
}

// PlanoDosSeparadores devolve o plano da experiência dos separadores: quatro braços sobre T1,
// 53 amostras por braço, UM turno e UMA tentativa por amostra — 212 pedidos no total.
//
// Um só turno porque a métrica é do primeiro turno (o modelo pediu a tool pelo mecanismo nativo
// ou não), e assim cada amostra custa exactamente um pedido. Um run que pediu a tool acaba em
// `turnos_esgotados`: é o desfecho esperado do braço que funciona, não uma falha.
func PlanoDosSeparadores(semente uint64) Plano {
	return Plano{
		Experiencia: ExperienciaSeparadores, Amostras: AmostrasDosSeparadores, Semente: semente,
		Tentativas: 1, MaxTurnos: 1, Casos: []string{CasoDosSeparadores}, Bracos: Bracos(),
	}
}

// ErrPlano — o plano não é utilizável.
var ErrPlano = errors.New("banco-ensaio: plano de corrida invalido")

func (p Plano) validar(b *Bateria) error {
	if p.Experiencia != ExperienciaBateria && p.Experiencia != ExperienciaSeparadores {
		return fmt.Errorf("%w: experiencia desconhecida", ErrPlano)
	}
	if p.Amostras <= 0 || p.Amostras > 10000 || p.Tentativas <= 0 || p.Tentativas > 3 || p.MaxTurnos <= 0 || p.MaxTurnos > 16 {
		return fmt.Errorf("%w: amostras (1 a 10000), tentativas (1 a 3) e turnos (1 a 16) fora dos limites", ErrPlano)
	}
	if len(p.Casos) == 0 || len(p.Bracos) == 0 {
		return fmt.Errorf("%w: sem casos ou sem bracos", ErrPlano)
	}
	for _, id := range p.Casos {
		if _, ok := b.Caso(id); !ok {
			return fmt.Errorf("%w: caso desconhecido %q", ErrPlano, id)
		}
	}
	vistos := map[Braco]bool{}
	for _, br := range p.Bracos {
		if _, err := LerBraco(string(br)); err != nil || vistos[br] {
			return fmt.Errorf("%w: braco desconhecido ou repetido", ErrPlano)
		}
		vistos[br] = true
	}
	return nil
}

// PedidosMaximos é o número MÁXIMO de pedidos que a corrida pode fazer: por cada amostra, braço
// e nó, `Tentativas × MaxTurnos`. Na experiência dos separadores é o número exacto.
func (p Plano) PedidosMaximos(b *Bateria) (int64, error) {
	if err := p.validar(b); err != nil {
		return 0, err
	}
	var nos int64
	for _, id := range p.Casos {
		c, _ := b.Caso(id)
		nos += int64(len(c.Nos))
	}
	return int64(p.Amostras) * int64(len(p.Bracos)) * nos * int64(p.Tentativas) * int64(p.MaxTurnos), nil
}

// passo é uma unidade da sequência: um caso, num braço, numa amostra.
type passo struct {
	caso    Caso
	braco   Braco
	amostra int
}

// sequencia devolve a ordem da corrida. Os braços correm INTERCALADOS, em blocos: cada amostra
// é um bloco com todos os braços, numa ordem baralhada pela semente (blocos aleatorizados). Um
// braço nunca corre «de seguida»: uma deriva da rota ao longo da corrida — carga, hora, um
// limite de taxa — reparte-se pelos braços em vez de cair toda num.
func (p Plano) sequencia(b *Bateria) []passo {
	gerador := novoGerador(p.Semente)
	var out []passo
	for amostra := 0; amostra < p.Amostras; amostra++ {
		ordem := append([]Braco(nil), p.Bracos...)
		for i := len(ordem) - 1; i > 0; i-- {
			j := int(gerador.proximo() % uint64(i+1)) // #nosec G115 -- o resto e menor do que i+1, que e um int positivo
			ordem[i], ordem[j] = ordem[j], ordem[i]
		}
		for _, braco := range ordem {
			for _, id := range p.Casos {
				c, _ := b.Caso(id)
				out = append(out, passo{caso: c, braco: braco, amostra: amostra})
			}
		}
	}
	return out
}

// gerador é um gerador determinista (splitmix64) para a ordem dos braços. Não é criptográfico
// nem precisa de ser: a semente vai no relatório para a ordem se reproduzir.
type gerador struct{ estado uint64 }

func novoGerador(semente uint64) *gerador { return &gerador{estado: semente} }

func (g *gerador) proximo() uint64 {
	g.estado += 0x9e3779b97f4a7c15
	z := g.estado
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Os estados finais de uma corrida.
const (
	TerminouCompleta    = "completa"
	TerminouTecto       = "tecto_atingido"
	TerminouContador    = "contador_inutilizavel"
	TerminouInterrompid = "interrompida"
	// TerminouChaveRecusada — os primeiros pedidos da corrida foram todos recusados por
	// autenticação: a corrida parou para não gastar o tecto com uma chave que não serve.
	TerminouChaveRecusada = "chave_recusada"
	// TerminouSaldoInsuficiente — a conta do fornecedor não tem saldo ou quota: os primeiros
	// pedidos foram todos 429 com esse tipo, ou foi isso que a sonda recebeu.
	TerminouSaldoInsuficiente = "saldo_insuficiente"
	// TerminouLimiteDeRitmo — idem, com o tipo de limite de ritmo: uma pausa entre pedidos resolve.
	TerminouLimiteDeRitmo = "limite_de_ritmo"
	// TerminouSo429 — os primeiros pedidos foram todos 429, com um tipo que o banco não conhece.
	TerminouSo429 = "so_respostas_429"
	// TerminouSerieDe429 — uma série longa de 429 seguidos a meio da corrida.
	TerminouSerieDe429 = "serie_de_429"
	// TerminouModeloDesconhecido — a sonda: o fornecedor não conhece o modelo.
	TerminouModeloDesconhecido = "modelo_desconhecido"
	// TerminouSondaFalhou — a sonda não teve 200, por uma causa fora do vocabulário.
	TerminouSondaFalhou = "sonda_falhou"
)

// Respostas429QueAbortam é o número de respostas 429 seguidas, desde o primeiro pedido (sem um
// 200 ainda), que faz o modo com modelo real abortar.
const Respostas429QueAbortam = 3

// SerieDe429QueAborta é o comprimento da série de 429 seguidos que, a meio da corrida, a pára.
const SerieDe429QueAborta = 10

// RecusasDeChaveQueAbortam é o número de recusas de autenticação seguidas, desde o primeiro
// pedido, que faz o modo com modelo real abortar.
const RecusasDeChaveQueAbortam = 3

// Os modos de uma corrida.
const (
	ModoFalso = "falso"
	ModoProxy = "proxy"
	ModoReal  = "real"
)

// CfgDaCorrida configura uma corrida.
type CfgDaCorrida struct {
	Modo    string
	Plano   Plano
	Bateria *Bateria
	No      *NoDeEnsaio
	// Contador é o contador do dia. Obrigatório no modo com modelo real: sem ele a corrida
	// recusa arrancar.
	Contador *Contador
	// Rota identifica a rota no relatório.
	Rota RotaDoRelatorio
	// RegiaoDeclarada é a região que o dono declarou; vai para o relatório como declaração.
	RegiaoDeclarada string
	// Extra entra no digest da configuração (o roteiro do falso, a tabela de preços).
	Extra map[string]string
	// Relogio dá a data do relatório. nil ⇒ time.Now.
	Relogio func() time.Time
	// AbortarAposRecusasDeChave, quando positivo, pára a corrida se os PRIMEIROS pedidos — este
	// número deles, seguidos, desde o início — forem todos recusados por autenticação (401 ou
	// 403): a chave não serve, e continuar só gastava o tecto do dia. Zero ⇒ não aborta.
	AbortarAposRecusasDeChave int
	// AbortarApos429Iniciais, quando positivo, pára a corrida se os PRIMEIROS pedidos — este
	// número deles, desde o início — tiverem todos resposta 429. A causa sai do tipo do erro:
	// saldo insuficiente, limite de ritmo, ou só «429». Zero ⇒ não aborta.
	AbortarApos429Iniciais int
	// AbortarAposSerieDe429, quando positivo, pára a corrida ao fim deste número de respostas 429
	// SEGUIDAS, em qualquer ponto dela. Zero ⇒ não aborta.
	AbortarAposSerieDe429 int
	// Sondar faz UM pedido mínimo à rota antes do primeiro caso ([NoDeEnsaio.Sondar]). Conta no
	// tecto. Se não der 200, a corrida não começa: o relatório sai sem observações, com a causa.
	Sondar bool
	// Pausa é o intervalo entre dois passos da corrida. Zero ⇒ sem pausa. Serve para não bater
	// num limite de taxa do fornecedor; não entra no digest da configuração.
	Pausa time.Duration
	// Progresso, se presente, recebe uma linha a cada vinte passos: só contagens.
	Progresso io.Writer
}

// ErrModoRealSemTecto — o modo com modelo real não corre sem contador e tecto do dia.
var ErrModoRealSemTecto = errors.New("banco-ensaio: o modo com modelo real nao corre sem o contador e o tecto do dia")

// Correr corre o plano e devolve o relatório. Um relatório parcial é devolvido também quando a
// corrida pára a meio (tecto atingido, contador inutilizável, interrupção, chave recusada, conta
// sem saldo, limite de ritmo) ou quando a sonda não a deixa começar: [Relatorio.Terminou]
// diz porquê. Só há erro sem relatório quando a corrida nem chegou a arrancar.
func Correr(ctx context.Context, cfg CfgDaCorrida) (*Relatorio, error) {
	if cfg.Bateria == nil || cfg.No == nil {
		return nil, fmt.Errorf("%w: sem bateria ou sem no de ensaio", ErrPlano)
	}
	previstos, err := cfg.Plano.PedidosMaximos(cfg.Bateria)
	if err != nil {
		return nil, err
	}
	switch cfg.Modo {
	case ModoFalso, ModoProxy:
	case ModoReal:
		// FAIL-CLOSED: o modo real exige o contador, e o MESMO contador tem de ser o que o
		// transporte do nó de ensaio consulta antes de cada pedido.
		if cfg.Contador == nil || cfg.No.contador != cfg.Contador {
			return nil, ErrModoRealSemTecto
		}
	default:
		return nil, fmt.Errorf("%w: modo desconhecido", ErrPlano)
	}
	if cfg.Contador != nil {
		// Antes de começar: a corrida inteira — e a sonda, se houver — tem de caber no que resta
		// do tecto do dia.
		aEnviar := previstos
		if cfg.Sondar {
			aEnviar++
		}
		if err := cfg.Contador.Cabe(aEnviar); err != nil {
			return nil, err
		}
	}
	relogio := cfg.Relogio
	if relogio == nil {
		relogio = time.Now
	}

	var obs []Observacao
	terminou := TerminouCompleta
	var sonda *Sonda
	if cfg.Sondar {
		s := cfg.No.Sondar(ctx)
		sonda = &s
		if s.Resultado != SondaOK {
			// A rota não responde: nenhum caso corre. Gastou-se, no máximo, um pedido.
			return construirRelatorio(cfg, relogio().UTC(), previstos, terminouDaSonda(s), sonda, nil)
		}
	}
	vigia := novoVigiaDeErros()
	passos := cfg.Plano.sequencia(cfg.Bateria)
ciclo:
	for i, ps := range passos {
		if i > 0 && cfg.Pausa > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(cfg.Pausa):
			}
		}
		if ctx.Err() != nil {
			terminou = TerminouInterrompid
			break
		}
		saidas := map[string]string{}
		for _, no := range ps.caso.Nos {
			pedido := PedidoDeRun{Caso: ps.caso, No: no, Braco: ps.braco, Amostra: ps.amostra, MaxTurnos: cfg.Plano.MaxTurnos}
			switch {
			case no.EntradaDe != "":
				anterior, ok := saidas[no.EntradaDe]
				if !ok {
					// O nó de que este depende não concluiu: não há entrada, e o nó não corre.
					continue
				}
				pedido.Entrada, pedido.OrigemEntrada = []byte(anterior), no.EntradaDe
			case no.EntradaDoc != "":
				doc, _ := cfg.Bateria.Documento(no.EntradaDoc)
				pedido.Entrada, pedido.OrigemEntrada = doc, "bateria"
			}
			for tentativa := 1; tentativa <= cfg.Plano.Tentativas; tentativa++ {
				pedido.Tentativa = tentativa
				o, saida := cfg.No.Correr(ctx, pedido)
				obs = append(obs, o)
				semDuzentos := 0
				for _, status := range o.HTTP {
					tipo := ""
					if status != 0 && status != http.StatusOK {
						if semDuzentos < len(o.TiposDeErro) {
							tipo = o.TiposDeErro[semDuzentos]
						}
						semDuzentos++
					}
					vigia.ver(status, tipo)
				}
				if causa := vigia.causa(cfg); causa != "" {
					terminou = causa
					break ciclo
				}
				switch o.Desfecho {
				case DesfechoTectoAtingido:
					terminou = TerminouTecto
					break ciclo
				case DesfechoContador:
					terminou = TerminouContador
					break ciclo
				case DesfechoCumprido:
					saidas[no.ID] = saida
				}
				if !desfechoRepetivel(o.Desfecho) {
					break
				}
				// A nova tentativa de um run que não pediu a tool leva o aviso do kernel
				// (AOS-506); a de uma resposta vazia não leva aviso (AOS-510).
				pedido.AvisoDeNovaTentativa = o.Desfecho == string(agentruntime.OutcomeContractNoCall)
			}
		}
		if cfg.Progresso != nil && ((i+1)%20 == 0 || i == len(passos)-1) {
			fmt.Fprintf(cfg.Progresso, "banco-ensaio: %d de %d passos\n", i+1, len(passos))
		}
	}
	return construirRelatorio(cfg, relogio().UTC(), previstos, terminou, sonda, obs)
}

// digestDaConfiguracao é o digest de tudo o que, além da bateria e da rota, decide o que a
// corrida mede: o plano, o texto das variantes, as versões publicadas e o que vier em Extra.
func digestDaConfiguracao(p Plano, extra map[string]string) string {
	doc := map[string]any{
		"plano": p, "alias": AliasDaRota, "extra": extra,
		"trocas_b": fmt.Sprint(trocasDoBracoB), "trocas_c": fmt.Sprint(trocasDoBracoC),
		"assembly": agentruntime.AssemblyVersion140,
	}
	versoes := map[string]string{}
	for _, b := range Bracos() {
		versoes[string(b)] = VersaoPublicadaDoBraco(b)
	}
	doc["versoes"] = versoes
	var tools []string
	for _, t := range ToolsDoBanco() {
		tools = append(tools, digestDaTool(t))
	}
	doc["tools"] = tools
	cru, _ := json.Marshal(doc)
	soma := sha256.Sum256(cru)
	return "sha256:" + hex.EncodeToString(soma[:])
}
