# Housefold — Domain Model

**Status:** Draft for review  
**Date:** 25 September 2026  
**Depends on:** [Housefold Project Vision](Housefold_Project_Vision.md) and [Housefold System Architecture](Housefold_System_Architecture.md)  
**Scope:** Shared language and conceptual model for the home and Housefold operations. This document defines meaning and ownership; API schemas, database tables, and Go types belong in later specifications.

## 1. Purpose

Housefold needs one shared model so the HA adapter, runtime, automation, presence, interaction, telemetry, intelligence, and UI modules refer to the same things in the same way.

The model is divided into two related domains:

1. **Home domain:** the physical and behavioral model of the household.
2. **Platform domain:** the Housefold runtime, modules, permissions, and execution lifecycle.

A home entity is not a module capability. The first describes what a device or HA entity represents or can do; the second describes what an installed module is allowed to access through Housefold.

## 2. Ownership rules

| Concept | Authority / owner | Housefold's relationship |
| --- | --- | --- |
| Area, device, HA entity, raw state, HA service and HA scene | Home Assistant | Import, normalize, observe, and reference. Do not create a competing source of truth for HA-owned physical configuration. |
| Provider and source identity | The system that supplies the record, such as HA | Preserve source identity and provenance when mapping into Housefold. |
| Housefold automation definition and run | Housefold automation module/runtime | Own its Go implementation identity, version, execution state, and result. |
| Presence estimate, inferred context, learned pattern, recommendation | Housefold module that derives it | Store as a derived claim with evidence, method, time, confidence where meaningful, and provenance. |
| Housefold intent, interaction, command request, module permission, health | Housefold | Own within explicit user and module authority boundaries. |
| Device action outcome and resulting raw entity state | HA for device execution/state; Housefold for request/run history | Request via HA services, track the request and outcome, then reconcile with HA state. |

Housefold may add relationships and interpretations above HA's physical model. It must not silently rewrite HA's entity registry, raw state, or area/device ownership.

## 3. Home domain concepts

### 3.1 Area

An **Area** is a named physical or organizational zone from HA, such as a room or floor. Its identity and membership come from HA. Housefold uses area relationships to scope presence, controls, and explanations.

A Housefold behavioral grouping or inferred occupancy zone must be represented distinctly from an HA Area unless it is explicitly mapped to one. This avoids presenting an inference as HA configuration.

### 3.2 Device

A **Device** is a physical or logical unit registered by HA, often exposing multiple entities. A device may be assigned to an Area and may have one or more provider identifiers.

Housefold does not infer that every entity is a separate physical device. Bridges and adapters preserve HA's device/entity relationship when supplied.

### 3.3 Entity

An **Entity** is HA's addressable representation of a stateful or controllable part of the home, such as a light, motion sensor, media player, or temperature sensor. Its raw state, attributes, availability, entity ID, and provider metadata originate from HA.

Entity IDs and display names are not interchangeable: IDs identify source records; names are labels that can change. Housefold must preserve the HA source identity and retain any internal stable reference separately. Exact identifier and rename behavior remain an API/schema decision.

An entity can expose observations, support one or more capabilities, and participate in commands through HA services. An entity does not itself imply a physical device, room, person, or inferred meaning.

### 3.4 State

A **State** is a time-bounded representation of an entity's reported condition. A normalized state should retain:

- Value and relevant normalized attributes.
- Source entity and provider.
- Observation or update time, and receipt time if useful.
- Availability/quality information and whether the value may be stale.
- Source version or ordering metadata when available.

HA is authoritative for raw entity state. Housefold may cache the latest known state for local operation, but must mark uncertainty after connection gaps and reconcile against HA before treating cached values as current.

### 3.5 Capability (home capability)

A **home Capability** is a normalized function that an entity or device can perform or provide, such as `switch`, `brightness_control`, `temperature_measurement`, or `motion_detection`. Capabilities are derived from HA's entity domains, state attributes, and available service descriptions; they do not replace HA device compatibility.

A capability describes a function, not an authorization grant. Capability naming, parameter schemas, and derivation rules require a separate API contract. Unknown HA features must remain representable without forcing a guessed capability.

### 3.6 Service

A **Service** is an HA operation that can be requested against one or more entities or devices, such as turning on a light. Services define the available action surface and parameter constraints supplied by HA. Housefold adapts service requests into typed operations for its consumers but routes execution through HA.

### 3.7 Person and identity

A **Person** is a household member or known visitor represented in Housefold's household context. A person may link to an HA person entity or another trusted identity source. The identity mapping is sensitive data and must be access-controlled.

A name, face, device, or sensor reading is evidence about a person; it is not automatically proof of identity. Uncertain identification must remain uncertain in the model.

### 3.8 Observation

An **Observation** is evidence reported by a source at a particular time. Examples include a motion event, door contact change, device tracker update, manual UI action, or HA entity state change.

An observation records what the source reported, where and when it came from, and any quality or uncertainty supplied by that source. A motion observation is evidence of motion; it is not equivalent to a claim that a person is present.

### 3.9 Presence estimate

A **Presence Estimate** is a derived claim about whether a person or household is present in the home or an Area, potentially including occupancy or activity. It should include:

- Subject and spatial scope (person, household, Area, or Housefold-defined zone).
- Estimate such as present, absent, unknown, or transitional.
- Confidence or quality when the method supports it.
- Effective time, expiry/staleness policy, and derivation provenance.
- Supporting and contradicting observations where policy permits retention.

Absence of a recent observation is not by itself proof of absence. Presence must support unknown or stale states and must not masquerade as raw HA state.

### 3.10 Mode and scene

A **Mode** is a named behavioral context used by Housefold, such as `home`, `away`, `sleep`, or a user-defined household mode. Modes can be explicitly selected or inferred, but inferred modes must carry provenance and confidence. A mode may affect automation decisions but is not itself a device state.

A **Scene** is a provider-defined collection of desired device states or an equivalent HA scene resource. Housefold may reference and activate scenes through HA; it does not claim that a scene's desired values are current device state.

### 3.11 Intent

An **Intent** is the interpreted goal behind a user or system request, such as “make the living room comfortable for a film.” It may originate from text, voice, a button, an automation, or an API client.

Intent interpretation is separate from execution. An intent can resolve to a known mode, scene, or set of permitted commands; it does not directly call devices or grant authority. Ambiguity, confidence, actor, and confirmation requirements must remain visible to the interaction/authorization flow.

## 4. Events, requests, and outcomes

These terms describe different points in the lifecycle and must not be conflated.

| Concept | Meaning | Example |
| --- | --- | --- |
| **Observation** | Evidence reported by a source. | Motion sensor reports motion in the hall. |
| **Event** | A time-stamped fact that something happened in a system. | HA reports the hall motion entity changed state. |
| **Decision** | A reasoned result from a rule or policy based on available context. | Automation decides the hall light should turn on because it is dark and motion was observed. |
| **Command** | A requested operation with a target, parameters, actor, and authority context. | Request the hall light to turn on. |
| **Action attempt** | The execution lifecycle for carrying out a command. | Housefold submits an HA service call and waits for the defined outcome. |
| **State change** | A subsequent report of the resulting state from HA. | HA reports the light entity is on. |
| **Outcome** | The recorded success, failure, timeout, rejection, or unknown result of an action attempt. | HA accepted the service request; the resulting state is not yet confirmed. |

An accepted service request is not necessarily proof the physical device reached the requested state. Housefold should distinguish request acceptance from observed outcome and resulting state.

Events are facts and should be append-only at the conceptual level. Corrections arrive as newer facts or explicit corrections; they should not silently rewrite what the system previously observed. Durable ordering, replay, deduplication, and retention are not defined here.

## 5. Automation concepts

### Automation definition

An **Automation Definition** is a stable Housefold-owned identity for a native Go automation, with an implementation version and declared runtime requirements. The code defines its triggers, conditions, and requested actions using supported Housefold interfaces. It is not a YAML document, DSL, or generic execution IR.

The automation may subscribe to events/state, query permitted current context, and request commands. Its stable identity persists across code revisions so run history and deployment state remain attributable.

### Automation run

An **Automation Run** is one execution instance of an Automation Definition. It records the triggering event or schedule, relevant context references, start/end times, decision/execution status, commands issued, and outcome. Sensitive values should be referenced or redacted where possible rather than copied wholesale into logs.

### Trigger, condition, and decision

A **Trigger** identifies an observation, event, schedule, or other defined start condition. A **Condition** evaluates available state or context. A **Decision** records the result and reason before action. The exact programming API remains part of the automation specification.

Housefold should preserve enough causal links to explain: which trigger started a run, which conditions passed or failed, what decision was made, which commands were requested, and what outcomes were observed.

## 6. Interaction, notification, and authorization concepts

### Interaction

An **Interaction** is a user or system exchange with Housefold. It has an actor, channel (for example PWA, voice surface, button, or automation), input, interpreted intent if any, authorization context, and response/outcome. Raw voice or text may contain sensitive data and requires explicit retention handling.

### Notification

A **Notification** is information Housefold chooses to deliver to an actor or endpoint. It has content, purpose, urgency, recipient, selected channel, delivery state, and fallback behavior. It is distinct from an HA event and from a user intent. Channel routing and interruption policy belong in the interaction/notification specification.

### Actor and authority

An **Actor** is a person, module, runtime process, or authorized remote client responsible for a request or decision. An **Authority Grant** defines which operations and data that actor may access, for what scope and duration, and whether confirmation is required.

Authority should follow the request into the resulting decision, command, and action attempt so that the system can later explain who or what caused a change. A recommendation is not an authorization grant.

## 7. Platform domain concepts

### Housefold Instance

A **Housefold Instance** is one installed runtime associated with a household/HA deployment. It has its own configuration, identity, runtime version, and module inventory. *Rosehill* is the user's HA instance/household name; Housefold remains the product name.

### Module and module version

A **Module** is an independently installable Housefold capability package. It has a stable identity, version, manifest, requested capability set, configuration, lifecycle state, and health. Its package provenance and approval state are part of platform management, not the home domain.

### Module capability grant

A **Module Capability Grant** authorizes a module to use a defined runtime interface, such as subscribing to selected state/events or requesting a class of service actions. It is scoped and revocable. This is distinct from the home Capability concept in §3.5.

### Runtime health and deployment

**Runtime Health** reports the runtime or module's readiness, dependencies, and degraded state. A **Deployment** is the staged transition from one module version to another, with candidate validation, activation, rollback, and an execution lease where necessary. Health and deployment records belong to Housefold, not HA.

## 8. Relationships

The core physical and behavioral relationships are:

| Relationship | Example | Ownership / confidence |
| --- | --- | --- |
| Area contains Device | Living room contains a lamp device. | Imported from HA; provider-owned. |
| Device exposes Entity | Lamp device exposes a light entity. | Imported from HA; provider-owned. |
| Entity located in Area | Hall motion entity is assigned to the hall. | Imported from HA, if configured. |
| Entity provides Capability | Light entity supports on/off and brightness. | Derived from HA metadata and service descriptions; mapping has provenance. |
| Source reports Observation/Event | HA reports motion. | Source-attributed fact. |
| Observation supports Presence Estimate | Recent motion contributes to “possibly occupied.” | Housefold inference; provenance/confidence required. |
| Person has Presence Estimate in Area | Person may be present in the kitchen. | Housefold inference or explicit trusted input; sensitive. |
| Automation responds to Event | Motion event starts an automation run. | Housefold definition/run history. |
| Automation Run produces Decision | Darkness condition passes. | Housefold, with causal explanation. |
| Decision requests Command | Request light-on service. | Housefold actor/authority attached. |
| Command uses Service on Entity | Call HA light turn-on for entity. | HA service contract; Housefold tracks attempt. |
| Action Attempt followed by Event/State | HA later reports light on. | HA reports result; Housefold links it to the attempt when correlation is possible. |
| Intent resolves to Mode, Scene, or Commands | “Movie mode” resolves to a known mode and permitted actions. | Housefold interpretation; confidence/confirmation policy applies. |

Relationships are typed and carry source/provenance where needed. The model should not permit arbitrary edges to turn correlation into causation or an inference into an HA fact.

## 9. Time, identity, and uncertainty

- Use stable source-qualified identity for provider records. Display names may change and must not be the sole key.
- Preserve source timestamps and distinguish them from Housefold receipt and processing times.
- Treat stale, unavailable, unknown, and absent as different states where the concept supports them.
- Represent inferred facts with method, evidence references, effective time, and confidence/quality where available.
- Preserve causal links from observations to inferences, decisions, commands, and outcomes.
- Avoid copying personal or raw device data into multiple stores without a defined retention need.

Exact ID formats, clock semantics, confidence scales, event ordering, and retention are implementation contracts still to be decided.

## 10. Model invariants

1. HA owns the raw Area/Device/Entity registry, raw entity state, and HA service execution.
2. Housefold imports HA's physical configuration and does not maintain a competing version of it.
3. A home Capability describes a device function; a module capability grant describes authorization. They are separate types and vocabularies.
4. An Observation is evidence; a Presence Estimate or Mode inference is a derived claim, not a raw sensor fact.
5. A Command is a request. An accepted request, an action attempt, an observed state change, and a successful physical outcome are not interchangeable.
6. Intent interpretation does not execute actions or grant permission by itself.
7. Every consequential decision and action retains actor/provenance and enough causal history to explain its origin.
8. Unknown or stale information must remain expressible; the model must not force false certainty.
9. Personal identity, presence history, and household activity are sensitive and subject to least-privilege access and retention rules.
10. Modules use the normalized Housefold model and declared grants rather than reaching through to HA internals or other modules' private data.

## 11. Open questions for subsystem specifications

- Exact normalized representation of HA Areas, Devices, Entities, attributes, services, scenes, unavailable states, and provider-specific extensions.
- Whether and how Housefold-defined zones or household groupings relate to HA Areas.
- How device capability schemas are derived and versioned as HA integrations change.
- Person identity mapping, visitor handling, confidence, and privacy retention for presence evidence.
- Event envelope, timestamp semantics, ordering, deduplication, replay, and durable journal policy.
- Automation API, run correlation, timers/state ownership, and action confirmation model.
- Mode lifecycle, explicit versus inferred mode authority, and precedence when signals conflict.
- Interaction and notification recipient/channel schemas and delivery guarantees.
- Module capability grant vocabulary and authorization/confirmation policy.

Resolve these incrementally through the HA adapter, automation, presence, interaction, telemetry, and security specifications. Do not encode unresolved choices as universal assumptions in the shared domain model.

