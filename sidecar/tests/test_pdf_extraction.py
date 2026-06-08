import pytest
from unittest.mock import patch, MagicMock
from services.pdf_extraction_service import extract_pdf_content, extract_year, extract_doi, _llm_fallback_extract

def test_pdf_extraction_patterns():
    # Test Year extraction
    assert extract_year("Published: 2021") == 2021
    assert extract_year("Copyright 2023") == 2023
    assert extract_year("(1999)") == 1999
    
    # Test DOI extraction
    assert extract_doi("Find this paper at 10.1038/s41586-020-2649-2.") == "10.1038/s41586-020-2649-2"

def test_pdf_extraction_fallback_no_text():
    # Test fallback extraction with invalid PDF bytes
    invalid_bytes = b"not a real pdf"
    res = extract_pdf_content(invalid_bytes, "Attention_Is_All_You_Need_2017.pdf")
    
    assert res["paper"]["publishDate"]["year"] == 2017
    # Should use fallback title based on filename
    assert "Attention Is All You Need 2017" in res["paper"]["title"]
    assert len(res["chunks"]) == 0
    assert res["pageCount"] == 0
    assert res["pages"] == 0
    assert res["full_text"] == res["fullText"]
    # No LLM should be triggered if there's no first_page_text
    assert res["extractionInfo"]["usedLlmFallback"] is False

def test_llm_fallback_extract_success():
    class MockExtraction:
        def __init__(self, e_class, text):
            self.extraction_class = e_class
            self.extraction_text = text
            
    class MockResult:
        def __init__(self):
            self.extractions = [
                MockExtraction("title", "A Groundbreaking Study on AI"),
                MockExtraction("author", "Jane Doe"),
                MockExtraction("author", "John Smith"),
                MockExtraction("abstract", "This abstract proves everything."),
                MockExtraction("year", "2024"),
                MockExtraction("doi", "10.1234/5678"),
            ]

    class MockExampleData:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class MockDataExtraction:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    mock_lx = MagicMock()
    mock_lx.data.ExampleData = MockExampleData
    mock_lx.data.Extraction = MockDataExtraction
    mock_lx.extract.return_value = MockResult()

    with patch("services.pdf_extraction_service.importlib.import_module", return_value=mock_lx):
        res = _llm_fallback_extract("dummy first page text")
    
    assert res["title"] == "A Groundbreaking Study on AI"
    assert res["authors"] == ["Jane Doe", "John Smith"]
    assert res["abstract"] == "This abstract proves everything."
    assert res["year"] == 2024
    assert res["doi"] == "10.1234/5678"
    assert mock_lx.extract.call_count == 1

def test_llm_fallback_extract_exception():
    # Test that exception in langextract doesn't blow up the application
    class MockExampleData:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    class MockDataExtraction:
        def __init__(self, **kwargs):
            self.kwargs = kwargs

    mock_lx = MagicMock()
    mock_lx.data.ExampleData = MockExampleData
    mock_lx.data.Extraction = MockDataExtraction
    mock_lx.extract.side_effect = Exception("API Key Missing")

    with patch("services.pdf_extraction_service.importlib.import_module", return_value=mock_lx):
        res = _llm_fallback_extract("dummy text")
    assert res == {}

@patch("services.pdf_extraction_service._llm_fallback_extract")
def test_extract_pdf_content_triggers_fallback(mock_llm_fallback):
    # Tests that when regex fails to find DOI/Year, LLM is called
    mock_llm_fallback.return_value = {
        "title": "LLM Title",
        "authors": ["Author 1"],
        "abstract": "LLM Abstract",
        "year": 2025,
        "doi": "10.0000/llm"
    }
    
    # We will pass invalid PDF bytes, but manually mock out the fallback conditions
    # This requires creating a dummy PDF or mocking the text extraction part.
    # We can mock the regex to return nothing so the LLM fallback is triggered
    with patch("services.pdf_extraction_service._fast_regex_extract") as mock_regex:
        mock_regex.return_value = {
            "title": "Short", # < 5 characters triggers fallback too
            "doi": None,
            "year": None
        }
        
        # we need to simulate having some text so the fallback is actually called
        with patch("services.pdf_extraction_service.re.sub") as mock_sub:
            mock_sub.return_value.strip.return_value = "fake text"
            
            # Since fitz/pypdf error out, first_page_text will be "" if we don't mock it
            # Best way is to use a valid minimal pdf or mock fitz.
            
            # For simplicity, we can test extract_pdf_content by patching fitz
            with patch("fitz.open") as mock_fitz:
                import io
                class MockPage:
                    def get_text(self): return "This is page 1 text"
                class MockDoc:
                    def __init__(self): self.pages = [MockPage()]
                    def __len__(self): return 1
                    def __iter__(self): return iter(self.pages)
                    
                mock_fitz.return_value = MockDoc()
                
                res = extract_pdf_content(b"fake_pdf_bytes", "test.pdf")
                
                mock_llm_fallback.assert_called_once_with("This is page 1 text")
                assert res["paper"]["title"] == "LLM Title"
                assert res["paper"]["authors"] == ["Author 1"]
                assert res["paper"]["abstract"] == "LLM Abstract"
                assert res["paper"]["publishDate"]["year"] == 2025
                assert res["paper"]["doi"] == "10.0000/llm"
                assert res["extractionInfo"]["usedLlmFallback"] is True
