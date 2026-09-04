package audit

import (
	"context"
	"sync"
)

// Store é o armazenamento append-only (WORM-like) da hash-chain de audit. É
// DELIBERADAMENTE minimalista e NÃO expõe update nem delete: a única mutação
// possível é acrescentar (Append). Esta é a fronteira estável — produção liga
// storage WORM real (append-only imposto pelo storage) por trás desta mesma
// interface, sem alterar produtores nem verificadores (AOS-011 → EPIC-08).
type Store interface {
	// Append acrescenta um registo à cadeia da sua partição. O Store atribui o
	// AuditSeq (gapless, começa em 1), define o PrevHash (=EntryHash do anterior,
	// ou génese no primeiro) e calcula o EntryHash. Devolve o registo selado.
	Append(ctx context.Context, rec AuditRecord) (AuditRecord, error)
	// Read devolve os registos da partição com audit_seq em [from,to] inclusive,
	// por ordem de armazenamento. Um intervalo sem registos devolve vazio.
	Read(ctx context.Context, partition string, from, to uint64) ([]AuditRecord, error)
	// Head devolve o último audit_seq da partição (0 se vazia).
	Head(ctx context.Context, partition string) (uint64, error)
	// At devolve o registo de audit_seq exacto na partição.
	At(ctx context.Context, partition string, seq uint64) (AuditRecord, bool, error)
}

// MemStore é a implementação de referência in-memory do [Store]: append-only,
// segura para concorrência. Guarda os registos por partição em ordem de
// escrita. É a implementação MVP; produção substitui por storage WORM real.
type MemStore struct {
	mu    sync.RWMutex
	parts map[string][]AuditRecord
}

// NewMemStore constrói um MemStore vazio.
func NewMemStore() *MemStore {
	return &MemStore{parts: make(map[string][]AuditRecord)}
}

// Append implementa [Store.Append]: sela o registo na cadeia da partição.
func (s *MemStore) Append(ctx context.Context, rec AuditRecord) (AuditRecord, error) {
	// AOS-311 — simetria com [FileStore.Append]: um contexto morto não sela. Verificado à
	// entrada para que um teste sobre MemStore não passe pela razão errada (um sink que
	// ignora o ctx faria o fail-closed por prazo parecer verdadeiro sem o ser).
	if err := ctx.Err(); err != nil {
		return AuditRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	part := s.parts[rec.Partition]
	var prev []byte
	if len(part) == 0 {
		prev = GenesisHash(rec.Partition)
		rec.AuditSeq = 1
	} else {
		last := part[len(part)-1]
		prev = last.EntryHash
		rec.AuditSeq = last.AuditSeq + 1
	}
	rec.PrevHash = prev
	stampSchema(&rec)
	rec.EntryHash = ComputeEntryHash(prev, rec)

	// Cópia defensiva das slices mutáveis para que o chamador não altere o estado
	// selado por referência partilhada.
	rec = cloneRecord(rec)
	s.parts[rec.Partition] = append(part, rec)
	return cloneRecord(rec), nil
}

// Read implementa [Store.Read].
func (s *MemStore) Read(_ context.Context, partition string, from, to uint64) ([]AuditRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	part := s.parts[partition]
	out := make([]AuditRecord, 0, len(part))
	for _, r := range part {
		if r.AuditSeq >= from && r.AuditSeq <= to {
			out = append(out, cloneRecord(r))
		}
	}
	return out, nil
}

// Head implementa [Store.Head].
func (s *MemStore) Head(_ context.Context, partition string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	part := s.parts[partition]
	if len(part) == 0 {
		return 0, nil
	}
	return part[len(part)-1].AuditSeq, nil
}

// At implementa [Store.At].
func (s *MemStore) At(_ context.Context, partition string, seq uint64) (AuditRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.parts[partition] {
		if r.AuditSeq == seq {
			return cloneRecord(r), true, nil
		}
	}
	return AuditRecord{}, false, nil
}

// Partitions implementa [PartitionLister]: os nomes de todas as partições com
// registos, ordenados (determinismo ⇒ verificação reproduzível em [VerifyStore]).
func (s *MemStore) Partitions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedPartitions(s.parts)
}

// cloneRecord devolve uma cópia profunda dos campos mutáveis (slices/ponteiros)
// para preservar o isolamento append-only: nem o chamador nem o Store partilham
// slices de bytes que possam ser mutadas após a selagem.
func cloneRecord(r AuditRecord) AuditRecord {
	r.PrevHash = cloneBytes(r.PrevHash)
	r.EntryHash = cloneBytes(r.EntryHash)
	if len(r.Principal.DelegationChain) > 0 {
		ch := make([]DelegationHop, len(r.Principal.DelegationChain))
		copy(ch, r.Principal.DelegationChain)
		r.Principal.DelegationChain = ch
	}
	if len(r.Obligations) > 0 {
		obs := make([]Obligation, len(r.Obligations))
		for i, ob := range r.Obligations {
			o := Obligation{Type: ob.Type}
			if len(ob.Fields) > 0 {
				o.Fields = make([]string, len(ob.Fields))
				copy(o.Fields, ob.Fields)
			}
			if len(ob.Params) > 0 {
				o.Params = make(map[string]string, len(ob.Params))
				for k, v := range ob.Params {
					o.Params[k] = v
				}
			}
			obs[i] = o
		}
		r.Obligations = obs
	}
	if r.PayloadRef != nil {
		pr := *r.PayloadRef
		pr.ContentHash = cloneBytes(r.PayloadRef.ContentHash)
		r.PayloadRef = &pr
	}
	return r
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
