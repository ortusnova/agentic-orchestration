import pytest

from domain_models.ids import IdKind, created_at_ms, kind_of, new_id


def test_every_kind_has_a_unique_prefix():
    prefixes = [k.value for k in IdKind]
    assert len(prefixes) == len(set(prefixes))
    assert all("_" not in p for p in prefixes)


@pytest.mark.parametrize("kind", list(IdKind))
def test_an_id_names_its_kind(kind):
    value = new_id(kind)
    assert value.startswith(kind.value + "_")
    assert len(value) == len(kind.value) + 1 + 26
    assert kind_of(value) is kind


def test_an_id_carries_its_creation_time():
    assert created_at_ms(new_id(IdKind.WORKSTREAM, now_ms=1_763_600_000_123)) == 1_763_600_000_123


def test_ids_sort_by_creation_time():
    ids = [new_id(IdKind.SESSION, now_ms=ms) for ms in (5, 1_000, 1_763_600_000_000, 2**48 - 1)]
    assert sorted(ids) == ids


def test_two_ids_from_the_same_millisecond_differ():
    assert new_id(IdKind.TICKET, now_ms=7) != new_id(IdKind.TICKET, now_ms=7)


@pytest.mark.parametrize("value", ["ONS-77", "ws_", "ws_01JD3Z8Q6V4C7M2N9P0R5S8T1", "xx_01JD3Z8Q6V4C7M2N9P0R5S8T1W", "ws_01JD3Z8Q6V4C7M2N9P0R5S8TIW"])
def test_anything_else_is_not_an_id(value):
    with pytest.raises(ValueError):
        kind_of(value)
