// Package good é um CONSUMIDOR-EXEMPLO correcto para o lint de separação: toda a
// lógica é determinística e o único efeito externo é encapsulado numa activity e
// despachado via Dispatcher.Dispatch — nunca uma primitiva de I/O directa. NÃO é
// compilado pelo módulo (vive em testdata); serve só de entrada ao analisador.
package good

import "context"

// dispatcher é o ponto de composição (stub local — o real é activity.Dispatcher).
type dispatcher interface {
	Dispatch(ctx context.Context, act any) (any, error)
}

// activity descreve o efeito externo de forma declarativa (stub local).
type activity struct {
	ToolID string
	Input  []byte
}

// runTurn é a lógica determinística do loop: calcula o input e ROTEIA o efeito pela
// activity. Nenhum http.Get / os.Open aqui — o efeito só corre sob o Dispatcher.
func runTurn(ctx context.Context, d dispatcher, seed int) ([]byte, error) {
	// trabalho determinístico (puro): sem efeitos externos.
	payload := make([]byte, 0, seed)
	for i := 0; i < seed; i++ {
		payload = append(payload, byte('a'+i%26))
	}
	// o efeito externo vai encapsulado numa activity e mediado pelo Dispatcher.
	act := activity{ToolID: "http.fetch", Input: payload}
	res, err := d.Dispatch(ctx, act)
	if err != nil {
		return nil, err
	}
	_ = res
	return payload, nil
}
