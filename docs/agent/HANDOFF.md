# Handoff

Active assignment: complete the Runtime v1 productization queue in tasks.json.

Start with `python3 scripts/agent_tasks.py next`. PRODUCTIZATION.md is authoritative for the stakeholder's accepted live-ready behavior. Work independently through all ready tasks; routine implementation decisions are delegated.

The completed 2026-10-01 harness is historical foundation, not the finish line. Runtime v1 is complete only after V1P12 security, V1P13 soak and V1P14 exact-artifact clean-HAOS installation all PASS, followed by V1P15 publication/closure. No self-waivers exist in this harness.

The agent may create repositories and distribution infrastructure when permissions allow, publish tested artifacts when prerequisites pass, and freely operate disposable test environments. It must never touch the stakeholder's live HAOS instance or substitute mocks/Docker for final HAOS acceptance.
