# T017 Implementation Plan

1. Define the chain-state contract and in-memory reference implementation.
2. Add the durable MySQL reservation/accepted-state implementation and schema
   contract.
3. Refactor invoice builders and shared processor submission to use a
   reservation, stable prepared payload, and accepted atomic commit.
4. Replace three per-branch daemon consumers with one mixed-document branch
   consumer.
5. Add focused XML/processor tests and update README integration notes.
6. Run convergence, formatting, targeted/full tests, vet, and self-review.

The work is intentionally confined to the daemon repository. The backend T016
repository is an integration dependency, not an edit target.
