# T030 signed-UBL import specification

The feature is the side-effect-free `invoice.ImportService` boundary described
in `requirements.md` and `design.md`. It accepts exact supplier UBL bytes plus
provenance metadata, reuses the existing safe signed-UBL parser, and returns
canonical parsed data/evidence or a typed explicit failure. It never persists,
posts, mutates stock/accounting, or renders UI.

Success requires valid fixture mapping, byte/provenance retention, explicit
tamper/signature handling, unsupported-field retention, formatting, targeted
tests, and the repository-wide Go test suite. Provider trust roots and authentic
ZATCA transform fixtures are unavailable and remain an explicit limitation.
