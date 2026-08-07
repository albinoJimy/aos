package main

// LIMIARES DO CIRCUIT BREAKER DO AGENTE VIVO (AOS-080/081) — o que o torna EFICAZ.
//
// O mecanismo está cablado (52daec6) mas INERTE sem limiares: um provider que devolva
// tudo a zero desliga todos os sinais e o disjuntor nunca dispara.
//
// AS ESCOLHAS AQUI SÃO DELIBERADAS E ESTREITAS. Dos quatro sinais disponíveis, só se
// ligam por omissão os DOIS que se conseguem justificar sem conhecer a carga de trabalho:
//
//   - NO-PROGRESS (3 iterações estéreis): o run observado repetiu a MESMA tool call negada
//     16 vezes até esgotar MaxTurns. Três iterações sem progresso é cedo o suficiente para
//     poupar 13 turnos e tarde o suficiente para tolerar um retry legítimo seguido de
//     correcção. É o sinal com evidência directa.
//   - WALL-CLOCK (30 min): um tecto absoluto de tempo em `running`. Generoso para um run
//     longo, mas impede um run pendurado de viver para sempre.
//
// As VELOCIDADES DE QUEIMA (custo/s, tokens/s) ficam DESLIGADAS por omissão, e isso é uma
// escolha, não um esquecimento: um tecto de velocidade errado mata runs saudáveis, e o
// valor certo depende do modelo, do preço e do perfil da carga — coisas que este nó não
// sabe. Ficam expostas por configuração para quem tiver os dados.
//
// Um limiar <= 0 desliga o respectivo sinal (contrato de [breaker.Thresholds]).

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/breaker"
)

// Defaults dos sinais LIGADOS por omissão. Ver o cabeçalho para a justificação.
const (
	// DefaultBreakerMaxStaleIterations é o nº de iterações consecutivas sem progresso
	// que faz o disjuntor abrir.
	DefaultBreakerMaxStaleIterations = 3
	// DefaultBreakerMaxWallClock é o tecto absoluto de tempo em `running`.
	DefaultBreakerMaxWallClock = 30 * time.Minute
)

// ErrBadBreakerThresholds — um limiar está definido mas é inválido. Fail-closed na
// CONFIGURAÇÃO (o nó recusa arrancar), nunca no comportamento: um limiar mal escrito que
// fosse ignorado deixaria o operador convencido de que o disjuntor protege quando não
// protege.
var ErrBadBreakerThresholds = errors.New("aos: limiares do circuit breaker mal configurados — AOS_BREAKER_MAX_STALE_ITERATIONS (inteiro >= 0), AOS_BREAKER_MAX_WALL_CLOCK (duracao Go, ex. 30m), AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC e AOS_BREAKER_MAX_TOKENS_PER_SEC (numeros >= 0). 0 desliga o sinal respectivo")

// staticThresholds é um [breaker.ThresholdProvider] que devolve os MESMOS limiares para
// todas as classes de agente.
//
// PORQUE UNIFORME E NÃO POR-CLASSE: os limiares por-classe só fazem sentido com dados
// sobre o comportamento de cada classe, que não existem. Um mapa por-classe inventado
// daria a ilusão de calibração. A porta [breaker.ThresholdProvider] recebe a classe,
// pelo que passar a diferenciar mais tarde não muda nada a montante.
type staticThresholds struct{ th breaker.Thresholds }

// Thresholds implementa [breaker.ThresholdProvider].
func (s staticThresholds) Thresholds(string) breaker.Thresholds { return s.th }

// breakerThresholdsFromEnv resolve os limiares. Devolve (nil, nil) quando NENHUM sinal
// fica ligado — nesse caso o disjuntor não é composto (não se compõe um disjuntor cego).
func breakerThresholdsFromEnv() (breaker.ThresholdProvider, error) {
	stale, err := envInt("AOS_BREAKER_MAX_STALE_ITERATIONS", DefaultBreakerMaxStaleIterations)
	if err != nil {
		return nil, err
	}
	wall, err := envDuration("AOS_BREAKER_MAX_WALL_CLOCK", DefaultBreakerMaxWallClock)
	if err != nil {
		return nil, err
	}
	cost, err := envFloat("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", 0)
	if err != nil {
		return nil, err
	}
	tokens, err := envFloat("AOS_BREAKER_MAX_TOKENS_PER_SEC", 0)
	if err != nil {
		return nil, err
	}
	th := breaker.Thresholds{
		MaxStaleIterations:       stale,
		MaxWallClock:             wall,
		MaxCostMicroUSDPerSecond: cost,
		MaxTokensPerSecond:       tokens,
	}
	if stale <= 0 && wall <= 0 && cost <= 0 && tokens <= 0 {
		return nil, nil // todos desligados ⇒ disjuntor inerte ⇒ não se compõe
	}
	return staticThresholds{th: th}, nil
}

// envInt lê um inteiro >= 0. Vazio ⇒ o default.
func envInt(name string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s=%q", ErrBadBreakerThresholds, name, v)
	}
	return n, nil
}

// envDuration lê uma duração Go >= 0. Vazio ⇒ o default.
func envDuration(name string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%w: %s=%q", ErrBadBreakerThresholds, name, v)
	}
	return d, nil
}

// envFloat lê um número >= 0. Vazio ⇒ o default.
func envFloat(name string, def float64) (float64, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%w: %s=%q", ErrBadBreakerThresholds, name, v)
	}
	return f, nil
}
