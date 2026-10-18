from atrium import smtp


def test_split_addresses_ignores_empty_entries():
    assert smtp.split_addresses("a@example.test,, ,b@example.test") == ["a@example.test", "b@example.test"]
    assert smtp.split_addresses(" , a@example.test, ") == ["a@example.test"]
    assert smtp.split_addresses('"B, c" <b@example.test>,,a@example.test') == ["b@example.test", "a@example.test"]
    assert smtp.split_addresses(" , ") == []
    assert smtp.split_addresses(None) == []


def test_split_addresses_drops_non_addresses():
    assert smtp.split_addresses("not an address") == []
    assert smtp.split_addresses("bob, a@example.test") == ["a@example.test"]
    assert smtp.split_addresses("Some One <one@example.test>") == ["one@example.test"]
