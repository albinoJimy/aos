package main

// aos362_wire_sem_seccomp_test.go — o outro caminho por onde o perfil seccomp pode chegar ao
// runtime sem que a tabela `seccompEnforcementFor` (`substrate/sandbox/driver.go`) dê por isso
// (AOS-362 a, revisão adversarial).
//
// O confronto estrutural do pacote da sandbox vê quem LÊ `Spec.Seccomp`. Mas o executor real vive
// aqui, e a doc do dev-hardened diz que a produção precisa de propagar o perfil «no wire». No dia em
// que o wire o transportar, os drivers reais passam a poder impor, e a tabela — que hoje diz `none`
// para o Firecracker e o gVisor — passa a SUBdeclarar no WORM.

import (
	"reflect"
	"strings"
	"testing"
)

// TestAOS362_OWireDosExecutoresNaoTransportaOPerfil falha se algum campo do wire dos dois
// executores remotos mencionar seccomp, no nome ou na tag JSON. Quando isso acontecer de
// propósito, a tabela `seccompEnforcementFor` tem de mudar no mesmo commit, e este teste com ela.
func TestAOS362_OWireDosExecutoresNaoTransportaOPerfil(t *testing.T) {
	for _, tipo := range []reflect.Type{
		reflect.TypeOf(fcExecInput{}),
		reflect.TypeOf(gvExecInput{}),
	} {
		var percorrer func(tp reflect.Type, caminho string)
		percorrer = func(tp reflect.Type, caminho string) {
			for tp.Kind() == reflect.Pointer || tp.Kind() == reflect.Slice {
				tp = tp.Elem()
			}
			if tp.Kind() != reflect.Struct {
				return
			}
			for i := 0; i < tp.NumField(); i++ {
				f := tp.Field(i)
				aqui := caminho + "." + f.Name
				if strings.Contains(strings.ToLower(f.Name+" "+f.Tag.Get("json")), "seccomp") {
					t.Errorf("%s transporta o perfil seccomp para o guest — a tabela seccompEnforcementFor "+
						"(substrate/sandbox/driver.go) diz que este driver NÃO impõe; actualize-a no mesmo "+
						"commit, depois de provar a imposição", aqui)
				}
				percorrer(f.Type, aqui)
			}
		}
		percorrer(tipo, tipo.Name())
	}
}
