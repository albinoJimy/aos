package agentruntime

import "context"

// SteerSource é a PORTA (idioma porta-no-kernel + adaptador-no-pilar, AOS-060) pela
// qual o loop base consome o canal de controlo out-of-band (AOS-023) na FRONTEIRA DE
// FIM DE TURNO — sem o kernel importar o pacote `control` (o adaptador vive lá). É
// aditiva: sem [WithSteerSource] o loop não a consulta e o comportamento de AOS-013
// permanece byte-idêntico.
//
// A consulta é SEMPRE no fim do turno (com todas as activities confirmadas e antes do
// turno seguinte), nunca a meio — a semântica de pausa GRACIOSA de AOS-023.
type SteerSource interface {
	// GracefulPause é consultado na fronteira de fim-de-turno. Se um interrupt estiver
	// pendente para o run, MATERIALIZA a pausa durável (running→paused via a máquina de
	// estados, AOS-017/023) e devolve (true, nil) — o loop pára graciosamente. Devolve
	// (false, nil) para continuar. Um erro aborta o run fail-closed.
	GracefulPause(ctx context.Context, runID string) (paused bool, err error)

	// PendingCorrection devolve uma correcção humana out-of-band pendente (e true), ou
	// (nil, false). O loop injecta-a no tail do turno SEGUINTE como dado de CONTROLO
	// TRUSTED (taint=trusted, ver [tailFromCorrection]) — nunca como conteúdo untrusted.
	//
	// O `ctx` existe porque a ENTREGA pode ter de ser registada DURAVELMENTE (AOS-292): uma
	// implementação sobre o canal de controlo marca aqui a correcção como consumida, e sem
	// isso um restart repunha-a e o loop injectava-a segunda vez — mudando o prompt de um
	// turno já capturado e fazendo o replay divergir. Uma implementação puramente em memória
	// ignora-o.
	PendingCorrection(ctx context.Context, runID string) (correction []byte, ok bool)
}

// WithSteerSource liga um [SteerSource] ao loop: a partir daqui, o loop consulta o
// canal de controlo out-of-band na fronteira de fim-de-turno (pausa graciosa +
// injecção de correcção trusted, AOS-158). Um valor nil é ignorado (mantém o loop sem
// steer — comportamento AOS-013 inalterado).
func WithSteerSource(s SteerSource) Option {
	return func(rt *Runtime) {
		if s != nil {
			rt.steer = s
		}
	}
}
